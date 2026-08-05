package runner

import (
	"errors"
	"io"
	"os/exec"
)

// ErrProcessStartFailed 是进程未成功进入独立进程组时的稳定内部错误。
var ErrProcessStartFailed = errors.New("execution process start failed")

// StartedProcess 保存已启动命令、稳定 leader/PGID 和尚未交给 reader 的输出 pipes。
type StartedProcess struct {
	// Command 是已经成功调用 Start、但尚未 Wait 的命令。
	Command *exec.Cmd
	// PID 是用户命令进程组 leader 的进程 ID。
	PID int
	// PGID 是独立进程组 ID；成功启动时必须等于 PID。
	PGID int
	// Stdout 是标准输出 pipe 的读取端。
	Stdout io.ReadCloser
	// Stderr 是标准错误 pipe 的读取端。
	Stderr io.ReadCloser
}

// StartCommand 启动 specification 并确认独立进程组；失败路径会回收可能已创建的 child。
func StartCommand(spec CommandSpec) (StartedProcess, error) {
	return startCommand(spec)
}
