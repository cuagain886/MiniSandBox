//go:build linux

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveShellUsesFixedPriority 验证 bash 优先，并在 bash 缺失时确定性回退到 sh。
func TestResolveShellUsesFixedPriority(t *testing.T) {
	directory := t.TempDir()
	bash := filepath.Join(directory, "bash")
	sh := filepath.Join(directory, "sh")
	writeExecutable(t, bash)
	writeExecutable(t, sh)
	got, err := resolveShell([]string{bash, sh}, osShellFileOps)
	if err != nil || got != bash {
		t.Fatalf("bash priority: got %q, err %v", got, err)
	}
	if err := os.Remove(bash); err != nil {
		t.Fatalf("remove bash: %v", err)
	}
	got, err = resolveShell([]string{bash, sh}, osShellFileOps)
	if err != nil || got != sh {
		t.Fatalf("sh fallback: got %q, err %v", got, err)
	}
}

// TestResolveShellRejectsMissingDirectoryAndNonExecutable 覆盖全部缺失、目录和不可执行文件。
func TestResolveShellRejectsMissingDirectoryAndNonExecutable(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing")
	nonExecutable := filepath.Join(directory, "plain")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	for _, candidates := range [][]string{
		{missing},
		{directory},
		{nonExecutable},
		{missing, directory, nonExecutable},
	} {
		got, err := resolveShell(candidates, osShellFileOps)
		if got != "" || !errors.Is(err, ErrShellNotFound) {
			t.Fatalf("candidates %v: got %q, err %v", candidates, got, err)
		}
	}
}

// TestResolveShellDoesNotConsultEnvironment 验证 SHELL 与 PATH 不能改变固定候选结果。
func TestResolveShellDoesNotConsultEnvironment(t *testing.T) {
	t.Setenv("SHELL", "/attacker/shell")
	t.Setenv("PATH", "/attacker/bin")
	got, err := resolveShell([]string{filepath.Join(t.TempDir(), "missing")}, osShellFileOps)
	if got != "" || !errors.Is(err, ErrShellNotFound) {
		t.Fatalf("environment influenced resolver: got %q, err %v", got, err)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}
