// Package runnerclient 实现 sandboxd 到 runnerd 的内部协议客户端。
//
// 本模块通过每个 sandbox 独立的 Unix Socket 发送健康检查和执行请求，并处理
// SSE 事件流；它不向外暴露 runner 地址或任意路径反向代理。
package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"minisandbox/pkg/protocol"
)

// Client 通过单个 sandbox 的 Unix Socket 调用 runnerd。
type Client struct {
	httpClient    *http.Client
	baseURL       string
	authorization func() ([]byte, error)

	healthMu                sync.Mutex
	expectedProtocolVersion int
	healthCacheTTL          time.Duration
	healthCheckedAt         time.Time
	now                     func() time.Time
}

// New 创建绑定到指定 Unix Socket 和派生 token 的 runner 客户端。
func New(socketPath, token string) *Client {
	tokenCopy := token
	return &Client{
		httpClient: &http.Client{
			Transport: unixTransport(socketPath),
			Timeout:   30 * time.Second,
		},
		baseURL: "http://runner",
		authorization: func() ([]byte, error) {
			if strings.TrimSpace(tokenCopy) == "" {
				return nil, errors.New("runner bearer token is required")
			}
			return []byte(tokenCopy), nil
		},
		now: time.Now,
	}
}

// Health 验证当前 sandbox 的 runner 是否已就绪且与容器 label 声明的协议
// 版本精确一致，成功时返回当前 Linux netns identity。
func (c *Client) Health(ctx context.Context, expectedProtocolVersion int) (protocol.RunnerHealth, error) {
	if c == nil || c.authorization == nil {
		return protocol.RunnerHealth{}, errors.New("runner bearer token is required")
	}
	if expectedProtocolVersion <= 0 {
		return protocol.RunnerHealth{}, errors.New("expected runner protocol version must be positive")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/healthz",
		nil,
	)
	if err != nil {
		return protocol.RunnerHealth{}, err
	}
	response, err := c.do(request)
	if err != nil {
		return protocol.RunnerHealth{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.RunnerHealth{}, &StatusError{StatusCode: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return protocol.RunnerHealth{}, errors.New("runner health response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var health protocol.RunnerHealth
	if err := decoder.Decode(&health); err != nil {
		return protocol.RunnerHealth{}, errors.New("runner health response is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return protocol.RunnerHealth{}, errors.New("runner health response has trailing data")
	}
	if health.Status != "ok" || health.Service != "runnerd" || health.Version == "" {
		return protocol.RunnerHealth{}, errors.New("runner health service identity is invalid")
	}
	if health.ProtocolVersion != expectedProtocolVersion {
		return protocol.RunnerHealth{}, &ProtocolMismatchError{}
	}
	if err := protocol.ValidateRunnerNetNSIdentity(health.NetNSIdentity); err != nil {
		return protocol.RunnerHealth{}, fmt.Errorf("runner health netns identity: %w", err)
	}
	return health, nil
}

func (c *Client) do(request *http.Request) (*http.Response, error) {
	if c == nil || c.httpClient == nil || c.authorization == nil || request == nil {
		return nil, errors.New("runner client is not configured")
	}
	token, err := c.authorization()
	if err != nil || len(token) == 0 {
		clear(token)
		return nil, errors.New("runner bearer token is required")
	}
	defer clear(token)
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+string(token))
	return c.httpClient.Do(clone)
}

// String 返回不包含 token、socket path 或其他凭据的固定诊断文本。
func (*Client) String() string { return "runnerclient.Client{redacted}" }

// GoString 返回不包含 token、socket path 或其他凭据的固定 Go 格式文本。
func (*Client) GoString() string { return "runnerclient.Client{redacted}" }

// ProtocolMismatchError 表示 runner health 与容器 label 的协议版本不一致。
type ProtocolMismatchError struct{}

// Error 返回不回显不受信 health 内容的固定错误文本。
func (*ProtocolMismatchError) Error() string {
	return "runner protocol version mismatch"
}

// FailureReason 返回不可重试的稳定协议不匹配生命周期 reason。
func (*ProtocolMismatchError) FailureReason() string {
	return string(protocol.ErrorCodeRunnerProtocolMismatch)
}

// StatusError 表示 runner 返回了非成功 HTTP 状态码。
type StatusError struct {
	StatusCode int
}

// Error 返回不包含响应正文和秘密信息的安全错误文本。
func (e *StatusError) Error() string {
	data, _ := json.Marshal(map[string]int{"status_code": e.StatusCode})
	return "runner request failed: " + string(data)
}
