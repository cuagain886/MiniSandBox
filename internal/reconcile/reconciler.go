// Package reconcile 将持久化的 sandbox 期望状态收敛为 runtime 实际状态。
//
// 本模块负责幂等重试、按 sandbox 串行化、周期调度和 TTL 判断；具体 Docker
// 操作和业务授权分别由 runtime adapter 与 application 层承担。
package reconcile

import (
	"context"

	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

// Reconciler 将单个 sandbox 的期望状态幂等收敛到 runtime 实际状态。
type Reconciler struct {
	store   store.Store
	runtime runtimeport.Runtime
	locks   *KeyedLock
}

// New 使用持久化端口和 runtime 端口创建状态收敛器。
func New(s store.Store, r runtimeport.Runtime) *Reconciler {
	return &Reconciler{store: s, runtime: r, locks: NewKeyedLock()}
}

// Reconcile 对指定 sandbox 执行一次幂等收敛。
//
// 初始化骨架尚未实现具体状态转换，后续实现必须在崩溃重试时保持相同结果。
func (r *Reconciler) Reconcile(context.Context, string) error {
	return nil
}
