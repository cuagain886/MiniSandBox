package docker

import (
	"context"

	mobyclient "github.com/moby/moby/client"
)

// EgressEngine 是 network namespace sidecar 编排实际使用的附加 Docker API 集合。
// 与 Phase 1 Engine 分离可使 outbound 关闭时不扩大基础 runtime 的测试依赖。
type EgressEngine interface {
	// NetworkInspect 只读取得服务级 bridge 的 driver、labels 与 IPAM 事实。
	NetworkInspect(context.Context, string, mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error)
	// NetworkCreate 创建受管 user-defined bridge。
	NetworkCreate(context.Context, string, mobyclient.NetworkCreateOptions) (mobyclient.NetworkCreateResult, error)
	// ContainerInspect 只读取得 sidecar 状态与安全配置。
	ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error)
	// ContainerCreate 创建尚未启动的 namespace anchor。
	ContainerCreate(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error)
	// ContainerAttach 在 start 前建立唯一 bootstrap stdin。
	ContainerAttach(context.Context, string, mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error)
	// ContainerStart 启动已 attach 的 sidecar。
	ContainerStart(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error)
	// CopyFromContainer 只读取得 readiness attestation，不在容器内 exec。
	CopyFromContainer(context.Context, string, mobyclient.CopyFromContainerOptions) (mobyclient.CopyFromContainerResult, error)
}

// NetNSResolver 从 Docker init PID 解析宿主机可观察的 network namespace 身份。
type NetNSResolver interface {
	// Identity 返回 linux-netns:<device>:<inode>；不得返回符号链接原文或 host path。
	Identity(int) (string, error)
}
