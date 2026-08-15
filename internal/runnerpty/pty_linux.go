//go:build linux

package runnerpty

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"minisandbox/pkg/protocol"
)

// ptyBaseEnvironment 是 PTY 用户进程的最小基础环境；请求附加变量在
// 合并时过滤平台内部前缀，防止 runner 配置泄漏进终端。
var ptyBaseEnvironment = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"TERM=xterm-256color",
	"HOME=/tmp",
	"LANG=C.UTF-8",
}

// linuxProcess 是 ptyProcess 的 Linux 实现。
type linuxProcess struct {
	master      *os.File
	command     *exec.Cmd
	terminateMu sync.Mutex
	terminated  bool
	waitOnce    sync.Once
	exitCode    int
	waitErr     error
	// reaped 在 Wait 完成回收后关闭，供 Terminate 等待真正的进程退出。
	reaped chan struct{}
}

// spawnPTYProcess 为请求命令创建伪终端并启动会话进程。
//
// 子进程以 setsid 成为会话组长并以 slave 为 controlling terminal；runner
// 侧持有 master 并在启动后关闭 slave。进程沿用 runner 当前（非 root）
// 身份，cwd 解析拒绝 symlink 与 workspace 外路径。
func spawnPTYProcess(options StartOptions) (ptyProcess, error) {
	workingDirectory, err := resolvePTYCwd(options.WorkspaceRoot, options.Request.Cwd)
	if err != nil {
		return nil, ErrInvalidStart
	}
	environment, err := buildPTYEnvironment(options.Request.Env, options.MaxEnvVars)
	if err != nil {
		return nil, ErrInvalidStart
	}

	master, slave, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	command := exec.Command(options.Request.Argv[0], options.Request.Argv[1:]...)
	command.Dir = workingDirectory
	command.Env = environment
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	command.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	if err := pty.Setsize(master, &pty.Winsize{
		Rows: options.Request.Rows,
		Cols: options.Request.Cols,
	}); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("set initial pty size: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("start pty process: %w", err)
	}
	// 关闭 runner 侧 slave，保证 EOF 语义和 SIGHUP 能正确传播到会话。
	_ = slave.Close()
	return &linuxProcess{master: master, command: command, reaped: make(chan struct{})}, nil
}

// Resize 调整终端窗口尺寸。
func (p *linuxProcess) Resize(cols, rows uint16) error {
	return pty.Setsize(p.master, &pty.Winsize{Rows: rows, Cols: cols})
}

// WriteStdin 把字节写入 PTY 主设备。
func (p *linuxProcess) WriteStdin(chunk []byte) (int, error) {
	return p.master.Write(chunk)
}

// ReadOutput 从 PTY 主设备读取一块输出。
func (p *linuxProcess) ReadOutput(buffer []byte) (int, error) {
	return p.master.Read(buffer)
}

// Terminate 以 TERM→grace→KILL 终止完整进程组并等待回收。
//
// setsid 使子进程成为会话组长，负 PID 命中整个进程组；关闭 master 触发
// controlling terminal SIGHUP 兜底。实际回收由 Wait 完成，本方法等待
// reaped 信号，保证返回后不再有存活后代。
func (p *linuxProcess) Terminate(grace time.Duration) {
	p.terminateMu.Lock()
	if p.terminated {
		p.terminateMu.Unlock()
		<-p.reaped
		return
	}
	p.terminated = true
	processGroup := -p.command.Process.Pid
	_ = syscall.Kill(processGroup, syscall.SIGTERM)
	p.terminateMu.Unlock()

	_ = p.master.Close()
	select {
	case <-p.reaped:
		return
	case <-time.After(grace):
		_ = syscall.Kill(processGroup, syscall.SIGKILL)
		<-p.reaped
	}
}

// Wait 返回进程退出码；重复调用返回同一结果。
func (p *linuxProcess) Wait() (int, error) {
	p.waitOnce.Do(func() {
		err := p.command.Wait()
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				p.exitCode = exitError.ExitCode()
			} else {
				p.exitCode = -1
				p.waitErr = err
			}
		} else {
			p.exitCode = p.command.ProcessState.ExitCode()
		}
		close(p.reaped)
	})
	return p.exitCode, p.waitErr
}

// Close 释放 PTY 主设备。
func (p *linuxProcess) Close() error {
	p.terminateMu.Lock()
	defer p.terminateMu.Unlock()
	return p.master.Close()
}

// resolvePTYCwd 把 workspace 相对 cwd 解析为每层真实目录的绝对路径。
//
// "." 或空表示根；任何 symlink 段或越界结果都被拒绝，防止终端进程在
// workspace 之外启动。
func resolvePTYCwd(workspaceRoot, requested string) (string, error) {
	root := filepath.Clean(workspaceRoot)
	if !filepath.IsAbs(root) {
		return "", ErrInvalidStart
	}
	if requested == "" || requested == "." {
		return root, nil
	}
	if err := protocol.ValidateFilePath(requested); err != nil {
		return "", ErrInvalidStart
	}
	current := root
	for _, segment := range strings.Split(requested, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrInvalidStart
		}
	}
	return current, nil
}

// buildPTYEnvironment 合并基础环境与请求附加变量。
func buildPTYEnvironment(request map[string]string, maxVars int) ([]string, error) {
	if maxVars > 0 && len(request) > maxVars {
		return nil, ErrInvalidStart
	}
	environment := append([]string(nil), ptyBaseEnvironment...)
	for key, value := range request {
		if !validPTYEnvKey(key) {
			return nil, ErrInvalidStart
		}
		environment = append(environment, key+"="+value)
	}
	return environment, nil
}

// validPTYEnvKey 拒绝空键、含分隔符的键和平台内部前缀。
func validPTYEnvKey(key string) bool {
	if key == "" || strings.ContainsAny(key, "=") {
		return false
	}
	return !strings.HasPrefix(key, "MINISANDBOX_")
}
