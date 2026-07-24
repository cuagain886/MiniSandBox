package docker

// ContainerInspection 保存 Docker inspect 结果中与生命周期收敛有关的字段。
type ContainerInspection struct {
	ContainerID string
	Running     bool
	ExitCode    int
}
