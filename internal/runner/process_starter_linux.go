//go:build linux

package runner

import (
	"errors"
	"syscall"
)

func startCommand(spec CommandSpec) (StartedProcess, error) {
	if spec.Command == nil || spec.Stdout == nil || spec.Stderr == nil || spec.Command.Process != nil {
		spec.Close()
		return StartedProcess{}, ErrProcessStartFailed
	}
	configureProcessGroup(spec.Command)
	configureParentDeathSafety(spec.Command)
	if err := spec.Command.Start(); err != nil {
		cleanupFailedStart(spec)
		return StartedProcess{}, ErrProcessStartFailed
	}
	pid := spec.Command.Process.Pid
	if pid <= 0 {
		cleanupFailedStart(spec)
		return StartedProcess{}, ErrProcessStartFailed
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		cleanupFailedStart(spec)
		return StartedProcess{}, ErrProcessStartFailed
	}
	// Setpgid 在 fork/exec 边界由内核应用；极短命令可能在 Getpgid 前已经退出，
	// 此时 ESRCH 不否定已经建立过 PGID=PID 的事实，后续 waiter 仍负责回收。
	if err == nil && pgid != pid {
		cleanupFailedStart(spec)
		return StartedProcess{}, ErrProcessStartFailed
	}
	return StartedProcess{
		Command: spec.Command,
		PID:     pid,
		PGID:    pid,
		Stdout:  spec.Stdout,
		Stderr:  spec.Stderr,
	}, nil
}

func cleanupFailedStart(spec CommandSpec) {
	if spec.Command != nil && spec.Command.Process != nil {
		_ = spec.Command.Process.Kill()
		_ = spec.Command.Wait()
	}
	spec.Close()
}
