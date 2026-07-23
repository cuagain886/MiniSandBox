package docker

type CreateOptions struct {
	SandboxID    string
	Image        string
	Workspace    string
	RunnerSocket string
	Labels       map[string]string
}
