//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func run(args []string) int {
	command := exec.Command(args[0], args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := command.Start(); err != nil {
		return 127
	}

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
