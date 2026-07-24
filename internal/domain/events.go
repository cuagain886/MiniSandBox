package domain

// SandboxChanged 表示 sandbox 的期望状态修订已发生变化，需要重新收敛。
type SandboxChanged struct {
	SandboxID string
	Revision  uint64
}
