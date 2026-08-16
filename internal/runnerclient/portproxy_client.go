package runnerclient

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"minisandbox/pkg/protocol"
)

// Proxy 把一次 HTTP 请求转发到当前 sandbox 的固定端口代理。
//
// port 与 pathAndQuery 只进入固定内部路由；调用方不能提供 runner URL。
// 成功转发时返回的响应带 ProxiedResponseHeader 标记，Body 由调用方关闭；
// 基础设施失败返回 StatusError。
func (c *Client) Proxy(
	ctx context.Context,
	port int,
	method string,
	pathAndQuery string,
	header http.Header,
	body io.Reader,
) (*http.Response, error) {
	request, err := c.newRequest(
		ctx,
		method,
		"/v1/ports/"+strconv.Itoa(port)+"/http"+pathAndQuery,
		body,
	)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := c.do(request)
	if err != nil {
		return nil, err
	}
	if response.Header.Get(protocol.ProxiedResponseHeader) != "" {
		return response, nil
	}
	defer response.Body.Close()
	return nil, decodeStatusError(response)
}
