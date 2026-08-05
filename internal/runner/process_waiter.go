package runner

import (
	"errors"
	"os/exec"
	"time"
)

const internalExecutionErrorCode = "INTERNAL_ERROR"
const internalExecutionErrorMessage = "execution failed"

// ProcessWaitOutcome 同时携带主进程 wait 候选和独立 pipe failure 候选。
type ProcessWaitOutcome struct {
	// WaitCandidate 把可解释 wait status 映射为 exited，否则映射为内部 failed。
	WaitCandidate TerminalCandidate
	// PipeFailure 在 reader 缺失、重复或返回非 EOF 错误时提供独立内部失败候选。
	PipeFailure *TerminalCandidate
}

// WaitProcess 先等待 stdout/stderr reader 完成，再由当前 supervisor 唯一调用 cmd.Wait。
// 这种顺序避免 Wait 提前关闭 StdoutPipe/StderrPipe 而截断仍在读取的尾部数据。
func WaitProcess(command *exec.Cmd, results <-chan PipeReadResult, startedAt time.Time, clock Clock) ProcessWaitOutcome {
	pipeFailure := waitForPipeResults(results)
	if command == nil || startedAt.IsZero() || clock == nil {
		candidate := internalFailureCandidate(0)
		return ProcessWaitOutcome{WaitCandidate: candidate, PipeFailure: pipeFailure}
	}
	waitErr := command.Wait()
	duration := clock.Now().UTC().Sub(startedAt.UTC())
	if duration < 0 {
		duration = 0
		waitErr = errors.New("execution clock moved backwards")
	}
	candidate := mapProcessWaitResult(waitErr, command.ProcessState, duration)
	if pipeFailure != nil {
		pipeFailure.Duration = duration
	}
	return ProcessWaitOutcome{WaitCandidate: candidate, PipeFailure: pipeFailure}
}

func waitForPipeResults(results <-chan PipeReadResult) *TerminalCandidate {
	seen := map[OutputStream]bool{}
	failed := results == nil
	for result := range results {
		if result.Stream != OutputStreamStdout && result.Stream != OutputStreamStderr || seen[result.Stream] {
			failed = true
			continue
		}
		seen[result.Stream] = true
		if result.Err != nil {
			failed = true
		}
	}
	if !seen[OutputStreamStdout] || !seen[OutputStreamStderr] {
		failed = true
	}
	if !failed {
		return nil
	}
	candidate := internalFailureCandidate(0)
	return &candidate
}

func mapProcessWaitResult(waitErr error, state processState, duration time.Duration) TerminalCandidate {
	if waitErr == nil {
		if exitCode, ok := normalizedProcessExitCode(state); ok {
			return exitedTerminalCandidate(exitCode, duration)
		}
		return internalFailureCandidate(duration)
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		if exitCode, ok := normalizedProcessExitCode(exitError.ProcessState); ok {
			return exitedTerminalCandidate(exitCode, duration)
		}
	}
	return internalFailureCandidate(duration)
}

type processState interface {
	ExitCode() int
	Sys() any
}

func exitedTerminalCandidate(exitCode int, duration time.Duration) TerminalCandidate {
	return TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode, Duration: duration}
}

func internalFailureCandidate(duration time.Duration) TerminalCandidate {
	return TerminalCandidate{
		Reason:    TerminationInternalFailure,
		ErrorCode: internalExecutionErrorCode,
		Message:   internalExecutionErrorMessage,
		Duration:  duration,
	}
}
