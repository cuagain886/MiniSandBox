package sdk

import (
	"context"
	"net/http"
	"net/url"

	"minisandbox/pkg/protocol"
)

func (c *Client) CreateSandbox(
	ctx context.Context,
	request protocol.CreateSandboxRequest,
) (protocol.Sandbox, error) {
	var sandbox protocol.Sandbox
	err := c.doJSON(ctx, http.MethodPost, "/v1/sandboxes", request, &sandbox)
	return sandbox, err
}

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

func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	return c.doJSON(
		ctx,
		http.MethodDelete,
		"/v1/sandboxes/"+url.PathEscape(id),
		nil,
		nil,
	)
}
