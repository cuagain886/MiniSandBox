package docker

type ResourceLimits struct {
	CPUQuotaBytes int64
	MemoryBytes   int64
	PIDs          int64
}
