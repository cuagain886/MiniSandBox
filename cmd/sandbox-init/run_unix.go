//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

type wait4Func func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error)

type reapResult struct {
	runnerStatus syscall.WaitStatus
	orphanCount  uint64
}

// run 以独立进程组启动容器主服务，并由单一 wait4 路径回收全部子进程。
func run(args []string) int {
	signals := make(chan os.Signal, 8)
	signal.Notify(
		signals,
		syscall.SIGCHLD,
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGHUP,
	)
	defer signal.Stop(signals)

	command := exec.Command(args[0], args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := command.Start(); err != nil {
		return 127
	}

	result, err := superviseRunner(command.Process.Pid, signals, syscall.Wait4)
	if err != nil {
		return 1
	}
	if result.runnerStatus.Exited() {
		return result.runnerStatus.ExitStatus()
	}
	return 1
}

// superviseRunner 处理 SIGCHLD 并保留既有的进程组信号转发行为。
//
// 本函数是 sandbox-init 唯一允许调用 wait4 的位置；exec.Cmd.Wait 不得与其
// 并存，否则其中一方会随机得到 ECHILD 并丢失 runner 退出状态。
func superviseRunner(
	runnerPID int,
	signals <-chan os.Signal,
	wait4 wait4Func,
) (reapResult, error) {
	var result reapResult
	for received := range signals {
		value, ok := received.(syscall.Signal)
		if !ok {
			continue
		}
		if value != syscall.SIGCHLD {
			_ = syscall.Kill(-runnerPID, value)
			continue
		}

		runnerReaped, err := drainChildren(runnerPID, wait4, &result)
		if err != nil {
			return reapResult{}, err
		}
		if runnerReaped {
			return result, nil
		}
	}
	return reapResult{}, errors.New("sandbox-init signal channel closed before runner was reaped")
}

// drainChildren 使用 WNOHANG 排空当前所有 zombie；runner 状态单独保存，其他
// PID 只增加孤儿计数，避免把用户子进程的状态误当成 runner 终态。
func drainChildren(
	runnerPID int,
	wait4 wait4Func,
	result *reapResult,
) (bool, error) {
	runnerReaped := false
	for {
		var status syscall.WaitStatus
		pid, err := wait4(-1, &status, syscall.WNOHANG, nil)
		switch {
		case err == nil && pid == 0:
			return runnerReaped, nil
		case err == nil && pid == runnerPID:
			result.runnerStatus = status
			runnerReaped = true
		case err == nil && pid > 0:
			result.orphanCount++
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ECHILD):
			if runnerReaped {
				return true, nil
			}
			return false, errors.New("runner child disappeared before its status was recorded")
		case err != nil:
			return false, err
		default:
			return false, errors.New("wait4 returned an invalid child PID")
		}
	}
}
