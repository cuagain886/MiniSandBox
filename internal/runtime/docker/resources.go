package docker

// ResourceLimits 描述映射到 Docker HostConfig 的资源上限。
type ResourceLimits struct {
	CPUQuotaBytes int64
	MemoryBytes   int64
	PIDs          int64
}
