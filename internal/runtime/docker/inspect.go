package docker

type ContainerInspection struct {
	ContainerID string
	Running     bool
	ExitCode    int
}
