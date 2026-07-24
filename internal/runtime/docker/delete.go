package docker

// DeleteOptions 描述容器清理策略。
type DeleteOptions struct {
	Force         bool
	RemoveVolumes bool
}
