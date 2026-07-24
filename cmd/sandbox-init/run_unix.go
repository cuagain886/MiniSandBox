//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// run 以独立进程组启动容器主服务，并把容器终止信号转发给整个进程组。
func run(args []string) int {
	command := exec.Command(args[0], args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := command.Start(); err != nil {
		return 127
	}

	// sandbox-init 作为 PID 1 必须主动转发信号，否则 Docker stop 只会通知 init，
	// runnerd 及其子进程无法进入正常的取消与清理流程。
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for received := range signals {
			if value, ok := received.(syscall.Signal); ok {
				_ = syscall.Kill(-command.Process.Pid, value)
			}
		}
	}()

	err := command.Wait()
	signal.Stop(signals)
	close(signals)
	<-done
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}
