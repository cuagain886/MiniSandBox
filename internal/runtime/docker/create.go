package docker

// CreateOptions 描述创建 sandbox 容器所需的运行时参数。
type CreateOptions struct {
	SandboxID    string
	Image        string
	Workspace    string
	RunnerSocket string
	Labels       map[string]string
}
