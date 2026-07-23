package sdk

import (
	"context"
	"net/http"
	"net/url"

	"minisandbox/pkg/protocol"
)

func (c *Client) Execute(
	ctx context.Context,
	sandboxID string,
	request protocol.ExecuteRequest,
) (protocol.ExecuteAccepted, error) {
	var accepted protocol.ExecuteAccepted
	err := c.doJSON(
		ctx,
		http.MethodPost,
		"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions",
		request,
		&accepted,
	)
	return accepted, err
}
