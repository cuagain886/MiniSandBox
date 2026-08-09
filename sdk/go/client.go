// Package sdk 提供面向 MiniSandbox 生命周期 API 的 Go 客户端。
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

	"minisandbox/pkg/protocol"
)

// Client 是 MiniSandbox 生命周期 API 的 Go 客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// ResponseError 表示服务端返回了符合公共协议的非成功 HTTP 响应。
type ResponseError struct {
	// StatusCode 是服务端返回的 HTTP 状态码。
	StatusCode int
	// Detail 是服务端返回的稳定公共错误详情。
	Detail protocol.ErrorDetail
}

// Error 返回包含公共错误码和安全消息的诊断文本。
func (e *ResponseError) Error() string {
	return fmt.Sprintf(
		"minisandbox: HTTP status %d: %s: %s",
		e.StatusCode,
		e.Detail.Code,
		e.Detail.Message,
	)
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
	return c.doJSONWithHeaders(
		ctx,
		method,
		path,
		requestBody,
		responseBody,
		nil,
	)
}

// doJSONWithHeaders 在统一 JSON 语义上附加调用点严格控制的请求头。
func (c *Client) doJSONWithHeaders(
	ctx context.Context,
	method string,
	path string,
	requestBody any,
	responseBody any,
	headers http.Header,
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
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope protocol.ErrorResponse
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			return fmt.Errorf(
				"minisandbox: HTTP status %d with invalid error response: %w",
				response.StatusCode,
				err,
			)
		}
		return &ResponseError{
			StatusCode: response.StatusCode,
			Detail:     envelope.Error,
		}
	}
	if responseBody == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(responseBody)
}
