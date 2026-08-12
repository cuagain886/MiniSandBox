// Package runtime 定义控制面管理 sandbox 实际资源所需的运行时端口。
//
// 本模块只描述与具体容器实现无关的能力和观测状态，Docker 等实现位于子包中。
package runtime

import (
	"context"

	"minisandbox/internal/domain"
)

// Runtime 定义 reconciler 管理 sandbox 实际资源所需的容器运行时能力。
type Runtime interface {
	// Ensure 幂等创建或修正资源，使其符合 sandbox 的期望规格。
	Ensure(context.Context, domain.Sandbox) (ActualSandbox, error)
	// Inspect 返回指定 sandbox 当前可观测的实际状态。
	Inspect(context.Context, string) (ActualSandbox, error)
	// Delete 幂等删除指定 sandbox 的全部 runtime 资源。
	Delete(context.Context, string) error
	// ListManaged 枚举带有 MiniSandbox 管理标识的全部实际资源，用于重启恢复。
	ListManaged(context.Context) ([]ActualSandbox, error)
}

// ComputeReplacer 是 Running 自动恢复可使用的唯一聚合替换端口。
//
// 实现必须保留已验证的 workspace volume 与 lease.json，只替换 main、可选 egress sidecar、
// runner socket、bootstrap 和 execution 临时数据；禁止把该操作实现为完整 Delete。
type ComputeReplacer interface {
	// ReplaceCompute 先关闭并移除 main，再移除可选 sidecar，最后按当前权威规格重建 compute。
	ReplaceCompute(context.Context, domain.Sandbox) (ActualSandbox, error)
}
