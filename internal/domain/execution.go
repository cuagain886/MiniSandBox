package domain

import "time"

// ExecutionSpec 描述容器内的一次命令执行。
//
// Argv 与 Shell 必须且只能设置一个；Timeout 为零表示使用服务端默认超时。
type ExecutionSpec struct {
	// Argv 是不经 shell 解释的参数数组，与 Shell 必须二选一。
	Argv []string
	// Shell 是显式交给 shell 解释的命令文本，与 Argv 必须二选一。
	Shell string
	// Env 是本次用户命令的附加环境变量。
	Env map[string]string
	// Cwd 是 workspace 内的执行目录；空值表示使用服务端默认目录。
	Cwd string
	// Timeout 是执行 deadline；零表示使用服务端默认超时。
	Timeout time.Duration
}

// Valid 判断执行规格是否满足 argv 与 shell 二选一的不变量。
func (s ExecutionSpec) Valid() bool {
	return (len(s.Argv) > 0) != (s.Shell != "") &&
		(len(s.Argv) == 0 || s.Argv[0] != "")
}
