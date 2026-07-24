package domain

import "time"

// ExecutionSpec 描述容器内的一次命令执行。
//
// Argv 与 Shell 必须且只能设置一个；Timeout 为零表示使用服务端默认超时。
type ExecutionSpec struct {
	Argv       []string
	Shell      string
	Env        map[string]string
	WorkingDir string
	Timeout    time.Duration
}

// Valid 判断执行规格是否满足 argv 与 shell 二选一的不变量。
func (s ExecutionSpec) Valid() bool {
	return (len(s.Argv) > 0) != (s.Shell != "")
}
