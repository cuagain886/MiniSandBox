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
	"time"

	"minisandbox/pkg/protocol"
)

// Client 通过单个 sandbox 的 Unix Socket 调用 runnerd。
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// New 创建绑定到指定 Unix Socket 和派生 token 的 runner 客户端。
func New(socketPath, token string) *Client {
	return &Client{
		httpClient: &http.Client{
			Transport: unixTransport(socketPath),
			Timeout:   30 * time.Second,
		},
		baseURL: "http://runner",
		token:   token,
	}
}

// Health 验证当前 sandbox 的 runner 是否已就绪且与容器 label 声明的协议
// 版本精确一致，成功时返回当前 Linux netns identity。
func (c *Client) Health(ctx context.Context, expectedProtocolVersion int) (protocol.RunnerHealth, error) {
	if c == nil || strings.TrimSpace(c.token) == "" {
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
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
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
