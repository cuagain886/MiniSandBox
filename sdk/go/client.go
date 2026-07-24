// Package sdk 提供面向 MiniSandbox 生命周期与执行 API 的 Go 客户端。
//
// 本模块向调用方暴露 Go 原生类型和稳定方法，负责 HTTP 映射但不重复实现控制面
// 业务规则。
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client 是 MiniSandbox 生命周期 API 的 Go 客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 使用控制面地址和可选 HTTP client 创建 SDK 客户端。
//
// httpClient 为 nil 时使用 http.DefaultClient；调用方可传入自定义 transport
// 配置认证、追踪和超时。
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// doJSON 负责普通 JSON API 的请求编码、状态检查和响应解码。
func (c *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	requestBody any,
	responseBody any,
) error {
	var body bytes.Buffer
	if requestBody != nil {
		if err := json.NewEncoder(&body).Encode(requestBody); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		c.baseURL+path,
		&body,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("minisandbox: unexpected HTTP status %d", response.StatusCode)
	}
	if responseBody == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(responseBody)
}
