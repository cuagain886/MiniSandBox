// Package docker 承载基于 Docker Engine 的 sandbox runtime adapter。
//
// 本模块设计上负责镜像、容器、workspace、labels 和 runner 注入；当前仅提供
// 接口骨架和数据结构。它不负责租户鉴权、配额策略或公共 API 错误码。
package docker

import (
	"context"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// Runtime 是 runtime 端口的 Docker Engine 实现。
type Runtime struct{}

// New 创建尚未绑定 Docker client 的初始化骨架。
func New() *Runtime {
	return &Runtime{}
}

// Ensure 幂等保证 Docker 资源符合 sandbox 期望状态。
//
// 当前初始化骨架尚未实现 Docker 调用。
func (r *Runtime) Ensure(
	context.Context,
	domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, domain.ErrNotImplemented
}

// Inspect 读取指定 sandbox 的 Docker 实际状态。
func (r *Runtime) Inspect(
	context.Context,
	string,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, domain.ErrNotImplemented
}

// Delete 幂等删除指定 sandbox 的 Docker 资源。
func (r *Runtime) Delete(context.Context, string) error {
	return domain.ErrNotImplemented
}

// ListManaged 按稳定 labels 枚举当前 daemon 中的受管容器。
func (r *Runtime) ListManaged(
	context.Context,
) ([]runtimeport.ActualSandbox, error) {
	return nil, domain.ErrNotImplemented
}

var _ runtimeport.Runtime = (*Runtime)(nil)
