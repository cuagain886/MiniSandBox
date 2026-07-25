package domain

// SandboxSpec 描述控制面已经补齐默认值、可持久化并用于运行时收敛的完整规格。
//
// 本类型不复用公共 API 请求模型，避免客户端省略字段后受服务端默认值变化影响。
type SandboxSpec struct {
	// Image 是 sandbox 使用的容器镜像引用。
	Image string
	// Resources 是实际应用到 sandbox 的 CPU、内存和进程数上限。
	Resources ResourceLimits
	// Workspace 是容器内工作目录的挂载语义。
	Workspace WorkspaceSpec
	// Network 是 sandbox 的网络访问能力。
	Network NetworkSpec
	// Platform 是 runner 和 init 产物必须匹配的目标平台。
	Platform Platform
}

// ResourceLimits 描述 sandbox 的强类型资源上限。
type ResourceLimits struct {
	// CPUQuotaMillis 是 CPU 配额的毫核数，500 表示 0.5 CPU。
	CPUQuotaMillis int64
	// MemoryMiB 是内存上限，单位为 MiB。
	MemoryMiB int64
	// PIDs 是容器内允许同时存在的进程数量上限。
	PIDs int64
}

// WorkspaceSpec 描述 sandbox 工作目录的容器内挂载语义。
type WorkspaceSpec struct {
	// MountPath 是工作目录在容器内的绝对路径，不得承载宿主机路径。
	MountPath string
	// Persistent 表示删除 sandbox 后是否保留工作目录数据。
	Persistent bool
}

// NetworkSpec 描述 sandbox 是否允许访问受控出站网络。
type NetworkSpec struct {
	// Outbound 表示是否允许通过受管网络访问容器外部。
	Outbound bool
}

// Platform 描述 sandbox 容器和嵌入式二进制的操作系统及架构。
type Platform struct {
	// OS 是 Go 风格的目标操作系统名称，例如 linux。
	OS string
	// Arch 是 Go 风格的目标架构名称，例如 amd64。
	Arch string
}
