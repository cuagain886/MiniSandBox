package sdk

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// PortHTTPRequest 是通过端口代理访问 sandbox 内 HTTP 服务的请求。
type PortHTTPRequest struct {
	// Method 是 HTTP 方法；不支持 CONNECT、TRACE 与 WebSocket 升级。
	Method string
	// Path 是以 "/" 开头的路径与查询串，例如 "/api/items?limit=10"。
	Path string
	// Header 是附加请求头；控制面认证头会自动剥离。
	Header http.Header
	// Body 是可选请求体；流式转发，不整体缓冲。
	Body io.Reader
}

// PortHTTP 把一次 HTTP 请求转发到当前 sandbox 的 loopback 服务。
//
// 目标固定为 sandbox 内 127.0.0.1:port；调用方不能指定 host 或 scheme。
// 返回标准 http.Response，Body 由调用方关闭。
func (s *Sandbox) PortHTTP(ctx context.Context, port int, request PortHTTPRequest) (*http.Response, error) {
	path := request.Path
	if path == "" {
		path = "/"
	}
	target := s.client.baseURL + "/v1/sandboxes/" + url.PathEscape(s.id) +
		"/ports/" + strconv.Itoa(port) + "/http" + path
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, target, request.Body)
	if err != nil {
		return nil, err
	}
	for name, values := range request.Header {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	return s.client.doStreamResponse(httpRequest)
}
