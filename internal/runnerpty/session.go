// Package runnerpty 实现 runner 内交互式 PTY 会话。
//
// 本包管理当前 sandbox 的 PTY 进程生命周期：启动带 controlling terminal
// 的会话进程、转发 stdin/输出/resize，并在进程退出、连接断开、超时或
// runner 关闭时终止完整进程组。本包不接触 WebSocket 协议细节，也不得
// 访问 Docker socket 或其他 sandbox。
package runnerpty

import (
	"context"
	"errors"
	"sync"
	"time"

	"minisandbox/pkg/protocol"
)

// PTY 会话的稳定错误集合。
var (
	// ErrUnsupported 表示平台不支持 PTY。
	ErrUnsupported = errors.New("pty capability unavailable")
	// ErrInvalidStart 表示 start 请求违反公共 PTY 协议。
	ErrInvalidStart = errors.New("invalid PTY start request")
	// ErrLimitReached 表示并发会话上限已满。
	ErrLimitReached = errors.New("PTY session limit reached")
	// ErrAlreadyFinished 表示会话已进入终态。
	ErrAlreadyFinished = errors.New("PTY session already finished")
)

// TerminalCause 是会话终态的内部归因。
type TerminalCause string

const (
	// TerminalCauseExited 表示用户进程正常完成 wait（含非零退出码）。
	TerminalCauseExited TerminalCause = "exited"
	// TerminalCauseCancelled 表示连接断开、runner 关闭或显式取消。
	TerminalCauseCancelled TerminalCause = "cancelled"
	// TerminalCauseTimedOut 表示会话总时长超过 deadline。
	TerminalCauseTimedOut TerminalCause = "timed_out"
)

// TerminalResult 是会话唯一终态结果，映射为 PTYServerEvent terminal 消息。
type TerminalResult struct {
	// Cause 是内部归因，不直接对外暴露。
	Cause TerminalCause
	// ExitCode 仅在 Cause 为 Exited 时非 nil。
	ExitCode *int
	// DurationMS 是从会话启动到终态的耗时。
	DurationMS int64
}

// ptyProcess 抽象平台 PTY 进程操作；生命周期由 Session 统一驱动。
type ptyProcess interface {
	// Resize 调整终端窗口。
	Resize(cols, rows uint16) error
	// WriteStdin 把字节写入 PTY 主设备。
	WriteStdin(p []byte) (int, error)
	// ReadOutput 从 PTY 主设备读取一块输出。
	ReadOutput(p []byte) (int, error)
	// Terminate 以 TERM→grace→KILL 终止完整进程组并回收。
	Terminate(grace time.Duration)
	// Wait 等待进程退出并返回退出码。
	Wait() (int, error)
	// Close 释放 PTY 主设备。
	Close() error
}

// Session 表示一条 PTY 连接及其用户进程。
//
// Session 由 Manager 创建；调用方通过 WriteStdin、Resize、Output 和
// Terminal 与会话交互。终态恰好发布一次，此后会话不能再被使用。
type Session struct {
	manager *Manager
	id      string

	process ptyProcess
	ctx     context.Context
	cancel  context.CancelFunc

	output   chan []byte
	terminal chan TerminalResult
	done     chan struct{}

	// cause 由取消方在 cancel 前设置，supervisor 读取后决定终态归因。
	causeMu sync.Mutex
	cause   TerminalCause

	startedAt time.Time
	// finishOnce 保证终态、资源释放和注销只执行一次。
	finishOnce sync.Once
}

// ID 返回不可预测的会话标识，仅用于日志关联。
func (s *Session) ID() string { return s.id }

// StartOptions 描述一次 PTY 启动。
type StartOptions struct {
	// Request 是公共 start 消息。
	Request protocol.PTYStartRequest
	// WorkspaceRoot 是容器内 workspace 绝对根路径。
	WorkspaceRoot string
	// DefaultTimeout 是请求未指定 timeout 时的默认时长。
	DefaultTimeout time.Duration
	// TerminationGrace 是 TERM 到 KILL 的等待时长。
	TerminationGrace time.Duration
	// MaxEnvVars 限定附加环境变量条目数。
	MaxEnvVars int
}

// Validate 校验启动选项的组合约束。
func (o StartOptions) Validate() error {
	if err := o.Request.Validate(); err != nil {
		return ErrInvalidStart
	}
	if o.WorkspaceRoot == "" || o.DefaultTimeout <= 0 || o.TerminationGrace <= 0 {
		return ErrInvalidStart
	}
	return nil
}

// Output 返回输出块通道；通道关闭表示不再有输出。
func (s *Session) Output() <-chan []byte { return s.output }

// Terminal 返回唯一终态结果通道。
func (s *Session) Terminal() <-chan TerminalResult { return s.terminal }

// Cancel 以指定归因终止会话；已终态时是安全 no-op。
func (s *Session) Cancel(cause TerminalCause) {
	s.causeMu.Lock()
	if s.cause == "" {
		s.cause = cause
	}
	s.causeMu.Unlock()
	s.cancel()
}

// finishWith 发布唯一终态并释放会话资源。
func (s *Session) finishWith(result TerminalResult) {
	s.finishOnce.Do(func() {
		result.DurationMS = time.Since(s.startedAt).Milliseconds()
		close(s.done)
		s.cancel()
		_ = s.process.Close()
		s.manager.remove(s)
		// 输出通道在读完剩余缓冲后由消费方自然结束；这里只保证不再生产。
		s.terminal <- result
		close(s.terminal)
	})
}
