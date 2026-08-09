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

// RunnerNetworkProbe 在普通 health 成功后返回同一响应中的受验证 netns identity。
type RunnerNetworkProbe interface {
	// ProbeNetwork 不接受 URL 或路径，且必须执行与 Probe 相同的精确协议校验。
	ProbeNetwork(context.Context, string, int) (string, error)
}

// RunnerShutdown 定义删除前关闭当前 sandbox execution 准入的固定端口。
type RunnerShutdown interface {
	// Shutdown 必须有界；错误表示尽力关闭失败，但不得阻止 runtime 永久清理。
	Shutdown(context.Context, string) error
}
