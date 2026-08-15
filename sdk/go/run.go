package sdk

import (
	"bytes"
	"context"
)

// Run 在当前 sandbox 中执行一次命令并等待其完整结束。
//
// 本方法组合 StartExecution、Wait 和日志读取：启动后台 execution、等待
// 任一合法终态、读取完整日志并自动区分 stdout/stderr。正常退出（退出码
// 为零）返回 RunResult 和 nil；非零退出返回 RunResult 和 *ExitError；
// 取消、超时和执行失败分别返回 RunResult 与对应的终态错误，调用方始终
// 可以拿到已收集的输出。
func (s *Sandbox) Run(ctx context.Context, request ExecuteRequest) (RunResult, error) {
	execution, err := s.StartExecution(ctx, request)
	if err != nil {
		return RunResult{}, err
	}
	info, err := execution.Wait(ctx)
	if err != nil {
		return RunResult{ExecutionID: execution.id, State: info.State}, err
	}

	result := RunResult{
		ExecutionID: execution.id,
		State:       info.State,
		ExitCode:    -1,
	}
	// newExecutionInfo 已校验终态信息必携带类型匹配的终止事件，这里可以
	// 安全地直接读取，不需要再做空指针防护。
	if info.State == ExecutionStateExited {
		result.ExitCode = info.TerminalEvent.ExitCode
	}
	result.Duration = info.TerminalEvent.Duration
	result.OutputTruncated = info.TerminalEvent.OutputTruncated

	var stdout, stderr bytes.Buffer
	logs := execution.Logs(ctx, 0)
	for logs.Next() {
		event := logs.Event()
		switch event.Type {
		case EventStdout:
			stdout.Write(event.Data)
		case EventStderr:
			stderr.Write(event.Data)
		}
	}
	if err := logs.Err(); err != nil {
		return result, err
	}
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()

	switch info.State {
	case ExecutionStateExited:
		if result.ExitCode != 0 {
			return result, &ExitError{ExecutionID: execution.id, ExitCode: result.ExitCode}
		}
		return result, nil
	case ExecutionStateCancelled:
		return result, &ExecutionCancelledError{ExecutionID: execution.id}
	case ExecutionStateTimedOut:
		return result, &ExecutionTimedOutError{ExecutionID: execution.id}
	case ExecutionStateFailed:
		return result, &ExecutionFailedError{
			ExecutionID: execution.id,
			ErrorCode:   info.TerminalEvent.ErrorCode,
			Message:     info.TerminalEvent.Message,
		}
	default:
		// Wait 只在四种终态返回，这里防御协议漂移而不是吞掉未知状态。
		return result, &ExecutionFailedError{
			ExecutionID: execution.id,
			ErrorCode:   "UNKNOWN_TERMINAL_STATE",
			Message:     string(info.State),
		}
	}
}
