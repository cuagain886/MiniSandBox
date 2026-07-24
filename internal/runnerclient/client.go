// Package runnerclient 实现 sandboxd 到 runnerd 的内部协议客户端。
//
// 本模块通过每个 sandbox 独立的 Unix Socket 发送健康检查和执行请求，并处理
// SSE 事件流；它不向外暴露 runner 地址或任意路径反向代理。
package runnerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
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

// Health 验证当前 sandbox 的 runner 是否已就绪。
func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/healthz",
		nil,
	)
	if err != nil {
		return err
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &StatusError{StatusCode: response.StatusCode}
	}
	return nil
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
