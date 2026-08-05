package runner

import (
	"reflect"
	"testing"
	"time"
)

// TestCommandBuilderPreservesArgvBoundaries 验证 argv 中的空格、引号和 shell 元字符不会被拼接或解释。
func TestCommandBuilderPreservesArgvBoundaries(t *testing.T) {
	request := ValidatedRequest{
		Argv:       []string{"missing-executable-for-spec-test", "a b", `"quoted"`, "*.go", ";echo"},
		Timeout:    time.Second,
		Background: true,
	}
	environment := []string{"A=1", "PATH=/usr/bin"}
	builder := newCommandBuilder(func() (string, error) {
		t.Fatal("argv mode consulted shell resolver")
		return "", nil
	})
	spec, err := builder.Build(request, t.TempDir(), environment)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	defer spec.Close()
	if !reflect.DeepEqual(spec.Command.Args, request.Argv) {
		t.Fatalf("argv: got %#v, want %#v", spec.Command.Args, request.Argv)
	}
	if spec.Command.Stdin != nil || spec.Stdout == nil || spec.Stderr == nil || spec.Stdout == spec.Stderr {
		t.Fatalf("stdio specification: stdin=%v stdout=%v stderr=%v", spec.Command.Stdin, spec.Stdout, spec.Stderr)
	}
	spec.Command.Args[1] = "changed"
	spec.Command.Env[0] = "A=changed"
	if request.Argv[1] != "a b" || environment[0] != "A=1" {
		t.Fatal("command specification shares caller slices")
	}
}

// TestCommandBuilderUsesResolvedShellWithoutParsingSource 验证 shell 模式严格构造 `<shell> -c <source>`。
func TestCommandBuilderUsesResolvedShellWithoutParsingSource(t *testing.T) {
	const source = `printf '%s' "$HOME;*.go"`
	builder := newCommandBuilder(func() (string, error) { return "/bin/test-shell", nil })
	cwd := t.TempDir()
	spec, err := builder.Build(ValidatedRequest{Shell: source, Timeout: time.Second}, cwd, []string{"HOME=/workspace"})
	if err != nil {
		t.Fatalf("build shell command: %v", err)
	}
	defer spec.Close()
	want := []string{"/bin/test-shell", "-c", source}
	if !reflect.DeepEqual(spec.Command.Args, want) || spec.Command.Dir != cwd || !reflect.DeepEqual(spec.Command.Env, []string{"HOME=/workspace"}) {
		t.Fatalf("shell specification: args=%#v dir=%q env=%#v", spec.Command.Args, spec.Command.Dir, spec.Command.Env)
	}
}

// TestCommandBuilderDefersMissingExecutableToStart 验证 argv[0] 不存在不会使纯 specification 构造失败。
func TestCommandBuilderDefersMissingExecutableToStart(t *testing.T) {
	builder := newCommandBuilder(func() (string, error) { return "/bin/sh", nil })
	spec, err := builder.Build(ValidatedRequest{Argv: []string{"definitely-not-a-minisandbox-command"}, Timeout: time.Second}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("missing executable rejected during build: %v", err)
	}
	spec.Close()
}

// TestCommandBuilderPropagatesShellResolutionFailure 验证 shell 缺失在创建任何 pipe 前返回稳定错误。
func TestCommandBuilderPropagatesShellResolutionFailure(t *testing.T) {
	builder := newCommandBuilder(func() (string, error) { return "", ErrShellNotFound })
	spec, err := builder.Build(ValidatedRequest{Shell: "echo ok", Timeout: time.Second}, t.TempDir(), nil)
	if err != ErrShellNotFound || spec.Command != nil || spec.Stdout != nil || spec.Stderr != nil {
		t.Fatalf("shell failure: spec=%+v err=%v", spec, err)
	}
}
