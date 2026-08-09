//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"minisandbox/internal/runnerbootstrap"
)

type wait4Func func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error)
type killFunc func(int, syscall.Signal) error

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
	// wait4 取代 Cmd.Wait 后，os.Process 仍可能持有 Linux pidfd；Release 只释放
	// Go 侧句柄而不会再次 wait，必须在所有 supervise 返回路径执行。
	defer command.Process.Release()

	result, err := superviseRunner(
		command.Process.Pid,
		signals,
		syscall.Wait4,
		syscall.Kill,
	)
	cleanupErr := releaseExecutionDirectoryOwner(runnerbootstrap.ExecutionDataDirectory)
	if err != nil || cleanupErr != nil {
		return 1
	}
	exitCode, err := runnerExitCode(result.runnerStatus)
	if err != nil {
		return 1
	}
	return exitCode
}

// releaseExecutionDirectoryOwner 在 runner 退出后把日志目录归还给 bind mount 父目录所有者。
// runner 运行期间目录仍严格属于降权 UID；只有 PID 1 收尾时才恢复宿主删除所需的遍历权限。
func releaseExecutionDirectoryOwner(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("execution directory path must be absolute")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return errors.New("runtime parent directory is unsafe")
	}
	stat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("runtime parent owner is unavailable")
	}
	// runtime 父目录在服务期为宿主 sandboxd 的 0700 目录；PID 1 没有 DAC_OVERRIDE，
	// 因此先用 CAP_CHOWN 临时取得遍历权，完成子目录归还后再恢复原 owner/mode。
	if err := os.Chown(parent, 0, 0); err != nil {
		return errors.New("acquire runtime parent directory failed")
	}
	restoreParent := func() error {
		if err := os.Chmod(parent, parentInfo.Mode().Perm()); err != nil {
			return err
		}
		return os.Chown(parent, int(stat.Uid), int(stat.Gid))
	}
	restored := false
	defer func() {
		if !restored {
			_ = restoreParent()
		}
	}()
	targetInfo, err := os.Lstat(path)
	if os.IsNotExist(err) {
		err := restoreParent()
		restored = err == nil
		return err
	}
	if err != nil || targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
		return errors.New("execution directory is unsafe")
	}
	if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
		return errors.New("restore execution directory owner failed")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("restore execution directory mode failed")
	}
	if err := restoreParent(); err != nil {
		return errors.New("restore runtime parent owner failed")
	}
	restored = true
	return nil
}

// runnerExitCode 把 Linux wait status 映射为 Docker 可观测的稳定 init 退出码。
//
// 普通退出保持 runner code；信号退出使用 shell/Docker 通用的 128+signal。
// stopped/continued 等非终态 status 表示 init 内部状态机错误，不能伪装成功。
func runnerExitCode(status syscall.WaitStatus) (int, error) {
	switch {
	case status.Exited():
		return status.ExitStatus(), nil
	case status.Signaled():
		return 128 + int(status.Signal()), nil
	default:
		return 0, errors.New("runner wait status is not terminal")
	}
}

// superviseRunner 处理 SIGCHLD 并保留既有的进程组信号转发行为。
//
// 本函数是 sandbox-init 唯一允许调用 wait4 的位置；exec.Cmd.Wait 不得与其
// 并存，否则其中一方会随机得到 ECHILD 并丢失 runner 退出状态。
func superviseRunner(
	runnerPID int,
	signals <-chan os.Signal,
	wait4 wait4Func,
	kill killFunc,
) (reapResult, error) {
	if runnerPID <= 0 {
		return reapResult{}, errors.New("runner PID must be positive")
	}
	var result reapResult
	for received := range signals {
		value, ok := received.(syscall.Signal)
		if !ok {
			continue
		}
		if value != syscall.SIGCHLD {
			if err := forwardRunnerSignal(runnerPID, value, kill); err != nil {
				return reapResult{}, err
			}
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

// forwardRunnerSignal 只把容器生命周期信号发送给 runner 的独立进程组。
//
// 负 PGID 保证不会误发给 sandbox-init 自己；runner 已先退出时 Kill 返回
// ESRCH 属于正常竞态，不能把 init 误判为内部失败。
func forwardRunnerSignal(
	runnerPID int,
	value syscall.Signal,
	kill killFunc,
) error {
	if runnerPID <= 0 {
		return errors.New("runner PID must be positive")
	}
	switch value {
	case syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP:
	case syscall.SIGCHLD:
		return nil
	default:
		return nil
	}
	if err := kill(-runnerPID, value); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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
