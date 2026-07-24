package docker

// Workspace 描述宿主机目录与容器内挂载路径的对应关系。
type Workspace struct {
	HostPath      string
	ContainerPath string
}
