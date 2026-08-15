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
	"io"
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
		return responseError(response.StatusCode, response.Body)
	}
	if responseBody == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(responseBody)
}

// doStream 执行请求；2xx 时调用可选 handle 处理响应，非 2xx 时解析
// 公共错误。上传下载等流式接口共用本语义。
func (c *Client) doStream(request *http.Request, handle func(*http.Response) error) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response.StatusCode, response.Body)
	}
	if handle == nil {
		return nil
	}
	return handle(response)
}

// doStreamBody 执行请求并直接返回响应体；调用方负责关闭。
func (c *Client) doStreamBody(request *http.Request) (io.ReadCloser, error) {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, responseError(response.StatusCode, response.Body)
	}
	return response.Body, nil
}

// responseError 把非 2xx 响应体转换为公共错误模型。
//
// JSON API 和 SSE 流式入口共用本函数，保证两类接口的错误语义一致。
func responseError(statusCode int, body io.Reader) error {
	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(body).Decode(&envelope); err != nil {
		return fmt.Errorf(
			"minisandbox: HTTP status %d with invalid error response: %w",
			statusCode,
			err,
		)
	}
	return &ResponseError{
		StatusCode: statusCode,
		Detail:     envelope.Error,
	}
}
