//go:build linux

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestValidateCWDAcceptsWorkspaceAndRealSubdirectory 验证默认 cwd、根目录和真实子目录被规范化接受。
func TestValidateCWDAcceptsWorkspaceAndRealSubdirectory(t *testing.T) {
	root := t.TempDir()
	subdirectory := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(subdirectory, 0o700); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	for _, request := range []struct {
		cwd  string
		want string
	}{
		{cwd: "", want: root},
		{cwd: root, want: root},
		{cwd: filepath.Join(root, "src", ".", "pkg"), want: subdirectory},
	} {
		got, err := ValidateCWD(root, request.cwd)
		if err != nil || got != request.want {
			t.Fatalf("cwd %q: got %q, err %v, want %q", request.cwd, got, err, request.want)
		}
	}
}

// TestValidateCWDRejectsEscapeMissingAndNonDirectory 覆盖 traversal、前缀碰撞、缺失路径和普通文件。
func TestValidateCWDRejectsEscapeMissingAndNonDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	other := filepath.Join(parent, "workspace-other")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatalf("create prefix collision: %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	for _, cwd := range []string{
		"relative",
		filepath.Join(root, ".."),
		filepath.Join(root, "..", filepath.Base(other)),
		other,
		filepath.Join(root, "missing"),
		file,
	} {
		if _, err := ValidateCWD(root, cwd); !errors.Is(err, ErrInvalidCWD) {
			t.Fatalf("unsafe cwd %q: %v", cwd, err)
		}
	}
}

// TestValidateCWDRejectsSymlinkAtEveryLevel 验证 workspace 根、中间组件和末端 symlink 均被拒绝。
func TestValidateCWDRejectsSymlinkAtEveryLevel(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-workspace")
	realTarget := filepath.Join(realRoot, "real", "child")
	if err := os.MkdirAll(realTarget, 0o700); err != nil {
		t.Fatalf("create real target: %v", err)
	}
	rootLink := filepath.Join(parent, "workspace-link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	middleLink := filepath.Join(realRoot, "middle-link")
	if err := os.Symlink(filepath.Join(realRoot, "real"), middleLink); err != nil {
		t.Fatalf("create middle symlink: %v", err)
	}
	leafLink := filepath.Join(realRoot, "leaf-link")
	if err := os.Symlink(realTarget, leafLink); err != nil {
		t.Fatalf("create leaf symlink: %v", err)
	}
	tests := []struct {
		root string
		cwd  string
	}{
		{root: rootLink, cwd: rootLink},
		{root: realRoot, cwd: filepath.Join(middleLink, "child")},
		{root: realRoot, cwd: leafLink},
	}
	for _, test := range tests {
		if _, err := ValidateCWD(test.root, test.cwd); !errors.Is(err, ErrInvalidCWD) {
			t.Fatalf("symlink cwd root=%q cwd=%q: %v", test.root, test.cwd, err)
		}
	}
}
