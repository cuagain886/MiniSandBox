package sdk

import (
	"context"
	"net/http"
	"net/url"

	"minisandbox/pkg/protocol"
)

// Execute 向指定 sandbox 提交命令并返回执行 ID。
//
// 当前初始化骨架只覆盖普通 JSON 响应，后续流式实现应继续通过协议层解码 SSE。
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
