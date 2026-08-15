package sdk

import (
	"context"
	"net/http"
)

// Health 查询 sandboxd 控制面是否存活。
//
// 存活探测只证明进程可响应，不代表可以服务请求；创建 sandbox 前应使用
// Readiness 确认必要组件就绪。非 2xx 响应返回 ResponseError。
func (c *Client) Health(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/healthz", nil, nil)
}
