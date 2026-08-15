package bootstrap

import (
	"context"
	"io"
	"net/http"

	"minisandbox/internal/application"
	"minisandbox/internal/runnerclient"
)

// applicationPortProxyFactory 把固定 Unix Socket runner factory 适配为
// 端口代理 application 端口。
type applicationPortProxyFactory struct {
	factory *runnerclient.Factory
}

// Client 返回绑定到指定 sandbox 的代理 client。
func (f applicationPortProxyFactory) Client(sandboxID string) (application.PortProxyClient, error) {
	client, err := f.factory.Client(sandboxID)
	if err != nil {
		return nil, err
	}
	return applicationPortProxyClient{client: client}, nil
}

// applicationPortProxyClient 把 runnerclient.Proxy 适配为 application 端口。
type applicationPortProxyClient struct {
	client *runnerclient.Client
}

// Proxy 转发一次 HTTP 请求到当前 sandbox 的固定端口代理。
func (c applicationPortProxyClient) Proxy(
	ctx context.Context,
	port int,
	method string,
	pathAndQuery string,
	header http.Header,
	body io.Reader,
) (*http.Response, error) {
	return c.client.Proxy(ctx, port, method, pathAndQuery, header, body)
}
