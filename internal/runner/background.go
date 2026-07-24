package runner

// BackgroundExecution 描述 runner 内存中跟踪的后台执行状态。
type BackgroundExecution struct {
	ID      string
	Running bool
}
