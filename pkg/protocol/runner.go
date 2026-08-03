package protocol

import "time"

// ExecuteRequest 是发送给 runnerd 的命令执行请求。
type ExecuteRequest struct {
	// Argv 是不经过 shell 解析的参数数组，与 Shell 必须二选一。
	Argv []string `json:"argv,omitempty"`
	// Shell 是显式请求 shell 解释的命令文本，与 Argv 必须二选一。
	Shell string `json:"shell,omitempty"`
	// Env 是本次用户命令的附加环境变量，runner 内部变量必须在合并前过滤。
	Env map[string]string `json:"env,omitempty"`
	// Cwd 是 sandbox 容器内的执行目录；缺失时由 runner 使用 /workspace。
	Cwd string `json:"cwd,omitempty"`
	// TimeoutSeconds 是执行超时秒数；零表示使用服务端默认值。
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
	// Background 表示请求创建可通过 status、logs 和 cancel 管理的后台执行。
	Background bool `json:"background,omitempty"`
}
