//go:build linux

package runner

import (
	"errors"
	"io"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestWaitProcessMapsZeroNonzeroAndSignalToExited 验证 0、1、127 和 signal 都是带稳定 exit code 的 exited。
func TestWaitProcessMapsZeroNonzeroAndSignalToExited(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		exitCode int
	}{
		{name: "zero", source: "exit 0", exitCode: 0},
		{name: "one", source: "exit 1", exitCode: 1},
		{name: "one hundred twenty seven", source: "exit 127", exitCode: 127},
		{name: "signal", source: "kill -TERM $$", exitCode: 128 + 15},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, readers := startWaitFixture(t, test.source)
			startedAt := time.Now().Add(-time.Second)
			outcome := WaitProcess(command, readers.Results, startedAt, fixedClock{value: time.Now()})
			candidate := outcome.WaitCandidate
			if candidate.Reason != TerminationProcessExited || candidate.ExitCode == nil || *candidate.ExitCode != test.exitCode || outcome.PipeFailure != nil || candidate.Duration < 0 {
				t.Fatalf("wait outcome: %+v", outcome)
			}
		})
	}
}

// TestWaitProcessClockRollbackDoesNotChangeExit 验证缺少单调读数的注入时钟即使
// 向后跳变，也只能把 duration 钳制为零，不能覆盖内核提供的真实退出状态。
func TestWaitProcessClockRollbackDoesNotChangeExit(t *testing.T) {
	command, readers := startWaitFixture(t, "exit 20")
	finishedAt := time.Now()
	outcome := WaitProcess(command, readers.Results, finishedAt.Add(time.Second), fixedClock{value: finishedAt})
	candidate := outcome.WaitCandidate
	if candidate.Reason != TerminationProcessExited || candidate.ExitCode == nil || *candidate.ExitCode != 20 ||
		candidate.Duration != 0 || outcome.PipeFailure != nil {
		t.Fatalf("clock rollback changed exit semantics: %+v", outcome)
	}
}

// TestWaitProcessWaitsForBothReaders 验证 cmd 已退出时仍会等待两个 reader result 后才返回。
func TestWaitProcessWaitsForBothReaders(t *testing.T) {
	command := exec.Command("/bin/true")
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	results := make(chan PipeReadResult, 2)
	done := make(chan ProcessWaitOutcome, 1)
	var clockCalled atomic.Bool
	clock := clockFunc(func() time.Time {
		clockCalled.Store(true)
		return time.Now()
	})
	go func() { done <- WaitProcess(command, results, time.Now(), clock) }()
	select {
	case <-done:
		t.Fatal("wait returned before reader results")
	case <-time.After(20 * time.Millisecond):
	}
	results <- PipeReadResult{Stream: OutputStreamStdout}
	results <- PipeReadResult{Stream: OutputStreamStderr}
	close(results)
	outcome := <-done
	if outcome.WaitCandidate.Reason != TerminationProcessExited || !clockCalled.Load() {
		t.Fatalf("delayed reader outcome: %+v", outcome)
	}
}

// TestWaitProcessReportsInternalWaitAndPipeFailures 验证不可解释 Wait 错误与 reader 错误分别形成内部失败候选。
func TestWaitProcessReportsInternalWaitAndPipeFailures(t *testing.T) {
	command := exec.Command("/bin/true")
	results := make(chan PipeReadResult, 2)
	results <- PipeReadResult{Stream: OutputStreamStdout, Err: errors.New("pipe failed")}
	results <- PipeReadResult{Stream: OutputStreamStderr}
	close(results)
	outcome := WaitProcess(command, results, time.Now(), fixedClock{value: time.Now()})
	if outcome.WaitCandidate.Reason != TerminationInternalFailure || outcome.PipeFailure == nil || outcome.PipeFailure.Reason != TerminationInternalFailure {
		t.Fatalf("internal failure outcome: %+v", outcome)
	}
	if outcome.WaitCandidate.Message == "pipe failed" || outcome.PipeFailure.Message == "pipe failed" {
		t.Fatal("internal cause leaked into terminal candidate")
	}
}

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

func startWaitFixture(t *testing.T, source string) (*exec.Cmd, *PipeReaders) {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", source)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	readers, err := StartPipeReaders(readCloser{stdout}, readCloser{stderr})
	if err != nil {
		t.Fatalf("start readers: %v", err)
	}
	go func() {
		for range readers.Chunks {
		}
	}()
	return command, readers
}

type readCloser struct{ io.ReadCloser }
