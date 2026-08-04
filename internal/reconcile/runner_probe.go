package reconcile

import "context"

// RunnerProbe 定义 reconciler 判断单个 sandbox runner 是否就绪的最小端口。
//
// 调用方只能传稳定 sandbox ID，不能指定 URL、socket 路径或 HTTP endpoint；
// adapter 必须把请求限制在当前 sandbox 的固定 `/healthz`。
type RunnerProbe interface {
	// Probe 对指定 sandbox 执行一次有界健康检查，不负责重试或更新 Store。
	Probe(context.Context, string, int) error
}
