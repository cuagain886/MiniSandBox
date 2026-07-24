package sdk

import (
	"context"
	"net/http"
	"net/url"

	"minisandbox/pkg/protocol"
)

// CreateSandbox 提交幂等的 sandbox 创建请求。
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
