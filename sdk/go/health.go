package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"minisandbox/pkg/protocol"
)

// Health 查询 sandboxd 控制面是否存活。
//
// 存活探测只证明进程可响应，不代表可以服务请求；创建 sandbox 前应使用
// Readiness 确认必要组件就绪。非 2xx 响应返回 ResponseError。
func (c *Client) Health(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/healthz", nil, nil)
}

// ReadinessComponent 描述单个必要组件的就绪状态。
type ReadinessComponent struct {
	// Name 是稳定的组件名称，例如 store、docker、artifact、recovery、worker。
	Name string
	// Ready 表示该组件是否已经满足服务请求的必要条件。
	Ready bool
}

// Readiness 是控制面及全部必要组件的公开就绪快照。
type Readiness struct {
	// Ready 仅在所有 Components 就绪时为 true。
	Ready bool
	// Components 按服务端固定顺序列出全部必要组件，不省略未就绪项。
	Components []ReadinessComponent
}

// Readiness 查询控制面及必要组件的就绪状态。
//
// 服务端未就绪时返回 503，但响应体仍携带完整组件状态；本方法把该状态
// 作为正常观测结果返回（Ready 为 false 且 err 为 nil），只有传输失败和
// 其他非预期状态码才返回错误。
func (c *Client) Readiness(ctx context.Context) (Readiness, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/readyz",
		nil,
	)
	if err != nil {
		return Readiness{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Readiness{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusServiceUnavailable {
		return Readiness{}, responseError(response.StatusCode, response.Body)
	}
	var payload protocol.ReadinessResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Readiness{}, fmt.Errorf(
			"minisandbox: decode readiness response: %w",
			err,
		)
	}
	readiness := Readiness{
		Ready:      payload.Status == protocol.ReadinessStatusReady,
		Components: make([]ReadinessComponent, 0, len(payload.Components)),
	}
	for _, component := range payload.Components {
		readiness.Components = append(readiness.Components, ReadinessComponent{
			Name:  string(component.Name),
			Ready: component.Status == protocol.ReadinessStatusReady,
		})
	}
	return readiness, nil
}
