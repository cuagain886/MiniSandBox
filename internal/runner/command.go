package runner

import (
	"errors"
	"io"
	"os/exec"
)

// ShellResolver 返回已经按固定策略验证过的 shell 绝对路径。
type ShellResolver func() (string, error)

// CommandBuilder 把已完成基础校验的请求转换为尚未启动的 os/exec specification。
type CommandBuilder struct {
	resolveShell ShellResolver
}

// CommandSpec 保存尚未启动的命令及彼此独立的 stdout/stderr 读取端。
type CommandSpec struct {
	// Command 是未调用 Start 的命令；不得使用 CommandContext 构造。
	Command *exec.Cmd
	// Stdout 是用户进程标准输出的独立 pipe 读取端。
	Stdout io.ReadCloser
	// Stderr 是用户进程标准错误的独立 pipe 读取端。
	Stderr io.ReadCloser
}

// NewCommandBuilder 创建使用固定 shell resolver 的 production builder。
func NewCommandBuilder() *CommandBuilder {
	return &CommandBuilder{resolveShell: ResolveShell}
}

func newCommandBuilder(resolveShell ShellResolver) *CommandBuilder {
	return &CommandBuilder{resolveShell: resolveShell}
}

// Build 设置 argv、cwd、env、closed stdin 和输出 pipes，但绝不启动或等待进程。
func (b *CommandBuilder) Build(request ValidatedRequest, cwd string, environment []string) (CommandSpec, error) {
	if b == nil || b.resolveShell == nil || cwd == "" {
		return CommandSpec{}, errors.New("command builder is not configured")
	}
	hasArgv := len(request.Argv) > 0
	hasShell := request.Shell != ""
	if hasArgv == hasShell {
		return CommandSpec{}, ErrInvalidExecutionRequest
	}
	var command *exec.Cmd
	if hasArgv {
		argv := append([]string(nil), request.Argv...)
		command = exec.Command(argv[0], argv[1:]...)
	} else {
		shell, err := b.resolveShell()
		if err != nil {
			return CommandSpec{}, err
		}
		command = exec.Command(shell, "-c", request.Shell)
	}
	command.Dir = cwd
	command.Env = append([]string(nil), environment...)
	command.Stdin = nil
	stdout, err := command.StdoutPipe()
	if err != nil {
		return CommandSpec{}, errors.New("create stdout pipe failed")
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return CommandSpec{}, errors.New("create stderr pipe failed")
	}
	return CommandSpec{Command: command, Stdout: stdout, Stderr: stderr}, nil
}

// Close 关闭尚未交给 pipe reader 的读取端；重复调用是安全的。
func (s CommandSpec) Close() {
	if s.Stdout != nil {
		_ = s.Stdout.Close()
	}
	if s.Stderr != nil {
		_ = s.Stderr.Close()
	}
}
