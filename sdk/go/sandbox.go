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
	minimumSandboxTTL = time.Minute
	maximumSandboxTTL = 24 * time.Hour
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

// CreateSandbox 提交 sandbox 创建请求。
func (c *Client) CreateSandbox(
	ctx context.Context,
	request protocol.CreateSandboxRequest,
) (protocol.Sandbox, error) {
	var sandbox protocol.Sandbox
	err := c.doJSON(ctx, http.MethodPost, "/v1/sandboxes", request, &sandbox)
	return sandbox, err
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
