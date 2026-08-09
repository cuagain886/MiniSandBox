package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"minisandbox/pkg/protocol"
)

const (
	minimumSandboxTTL    = time.Minute
	maximumSandboxTTL    = 24 * time.Hour
	idempotencyKeyHeader = "Idempotency-Key"
)

// CreateSandboxRequest 是 Go SDK 面向调用方的创建请求模型。
//
// TTL 使用 Go 原生 time.Duration；nil 表示不发送 ttl_seconds 并使用服务端默认值。
// 非 nil 值必须是 1 分钟到 24 小时闭区间内的整秒数。
type CreateSandboxRequest struct {
	// Image 是 sandbox 使用的容器镜像引用。
	Image string
	// TTL 是可选租约时长；nil 表示由服务端选择默认 TTL。
	TTL *time.Duration
	// Network 是可选 outbound 意图；nil 等价于 outbound=false。
	Network *SandboxNetworkRequest
}

// SandboxNetworkRequest 描述 SDK 调用方唯一可以选择的网络能力。
type SandboxNetworkRequest struct {
	// Outbound 表示是否请求受管公网出站能力。
	Outbound bool
}

// RenewSandboxRequest 是 Go SDK 面向调用方的续期请求。
type RenewSandboxRequest struct {
	// ExpiresAt 是请求的新绝对到期时间；SDK 会无损归一化为 UTC 后发送。
	ExpiresAt time.Time
}

// CreateSandboxOptions 描述不属于 JSON body 的可选创建语义。
type CreateSandboxOptions struct {
	// IdempotencyKey 在当前 local:v1 scope 内标识可安全重放的创建请求；空字符串表示不发送。
	IdempotencyKey string
}

// wire 把 SDK 原生 duration 映射为稳定的秒级创建协议。
func (r CreateSandboxRequest) wire() (protocol.CreateSandboxRequest, error) {
	var ttlSeconds *int64
	if r.TTL != nil {
		if *r.TTL < minimumSandboxTTL ||
			*r.TTL > maximumSandboxTTL ||
			*r.TTL%time.Second != 0 {
			return protocol.CreateSandboxRequest{}, fmt.Errorf(
				"minisandbox: sandbox TTL must be a whole number of seconds between 1m and 24h",
			)
		}
		seconds := int64(*r.TTL / time.Second)
		ttlSeconds = &seconds
	}

	var network *protocol.SandboxNetworkRequest
	if r.Network != nil {
		network = &protocol.SandboxNetworkRequest{Outbound: r.Network.Outbound}
	}
	return protocol.CreateSandboxRequest{
		Image:      r.Image,
		TTLSeconds: ttlSeconds,
		Network:    network,
	}, nil
}

// wire 校验非零绝对时间并归一化为 UTC wire model。
func (r RenewSandboxRequest) wire() (protocol.RenewSandboxRequest, error) {
	if r.ExpiresAt.IsZero() {
		return protocol.RenewSandboxRequest{}, fmt.Errorf(
			"minisandbox: sandbox expiration must not be zero",
		)
	}
	return protocol.RenewSandboxRequest{ExpiresAt: r.ExpiresAt.UTC()}, nil
}

// CreateSandbox 提交 sandbox 创建请求。
func (c *Client) CreateSandbox(
	ctx context.Context,
	request protocol.CreateSandboxRequest,
) (protocol.Sandbox, error) {
	return c.createSandbox(ctx, request, "")
}

// CreateSandboxWithOptions 使用 SDK 原生创建模型并附加可选幂等 key。
//
// 无 key 时与 CreateSandbox 相同，每次调用都保持非幂等创建语义。
func (c *Client) CreateSandboxWithOptions(
	ctx context.Context,
	request CreateSandboxRequest,
	options CreateSandboxOptions,
) (protocol.Sandbox, error) {
	wire, err := request.wire()
	if err != nil {
		return protocol.Sandbox{}, err
	}
	if options.IdempotencyKey != "" && !validIdempotencyKey(options.IdempotencyKey) {
		return protocol.Sandbox{}, fmt.Errorf(
			"minisandbox: idempotency key must contain 1 to 128 allowed ASCII characters",
		)
	}
	return c.createSandbox(ctx, wire, options.IdempotencyKey)
}

func (c *Client) createSandbox(
	ctx context.Context,
	request protocol.CreateSandboxRequest,
	idempotencyKey string,
) (protocol.Sandbox, error) {
	var sandbox protocol.Sandbox
	var headers http.Header
	if idempotencyKey != "" {
		headers = make(http.Header)
		headers.Set(idempotencyKeyHeader, idempotencyKey)
	}
	err := c.doJSONWithHeaders(
		ctx,
		http.MethodPost,
		"/v1/sandboxes",
		request,
		&sandbox,
		headers,
	)
	return sandbox, err
}

func validIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// GetSandbox 返回指定 sandbox 的当前生命周期状态。
func (c *Client) GetSandbox(
	ctx context.Context,
	id string,
) (protocol.Sandbox, error) {
	var sandbox protocol.Sandbox
	err := c.doJSON(
		ctx,
		http.MethodGet,
		"/v1/sandboxes/"+url.PathEscape(id),
		nil,
		&sandbox,
	)
	return sandbox, err
}

// RenewSandbox 请求把指定 sandbox 的租约延长到绝对 UTC 时间。
//
// 等于当前到期时间由服务端作为幂等 no-op 返回 200；缩短、已过期或已提交终止
// 意图由服务端返回稳定 409 错误。
func (c *Client) RenewSandbox(
	ctx context.Context,
	id string,
	request RenewSandboxRequest,
) (protocol.Sandbox, error) {
	wire, err := request.wire()
	if err != nil {
		return protocol.Sandbox{}, err
	}
	var sandbox protocol.Sandbox
	err = c.doJSON(
		ctx,
		http.MethodPost,
		"/v1/sandboxes/"+url.PathEscape(id)+"/renew",
		wire,
		&sandbox,
	)
	return sandbox, err
}

// DeleteSandbox 提交 sandbox 删除意图；重复删除应由服务端按幂等语义处理。
func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	return c.doJSON(
		ctx,
		http.MethodDelete,
		"/v1/sandboxes/"+url.PathEscape(id),
		nil,
		nil,
	)
}
