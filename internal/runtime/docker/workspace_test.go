package docker

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// prepareRuntimeRoot 创建模拟 P1-012 已完成的数据和 run 根目录。
func prepareRuntimeRoot(t *testing.T) string {
	t.Helper()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dataDirectory, "run"), 0o700); err != nil {
		t.Fatalf("prepare runtime root: %v", err)
	}
	return dataDirectory
}

// TestEnsureRuntimeDirectoryCreatesOnlyDirectory 验证路径、权限和 socket 不预创建。
func TestEnsureRuntimeDirectoryCreatesOnlyDirectory(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)

	paths, err := EnsureRuntimeDirectory(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("ensure runtime directory: %v", err)
	}
	if !paths.CreatedByThisCall {
		t.Fatal("first ensure did not report directory creation")
	}
	wantDirectory := filepath.Join(dataDirectory, "run", testSandboxID)
	if paths.Directory != wantDirectory {
		t.Fatalf("directory: got %q, want %q", paths.Directory, wantDirectory)
	}
	if got, want := paths.HostRunnerSocket,
		filepath.Join(wantDirectory, "runner.sock"); got != want {
		t.Fatalf("socket path: got %q, want %q", got, want)
	}

	info, err := os.Lstat(paths.Directory)
	if err != nil {
		t.Fatalf("inspect runtime directory: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe runtime directory mode: %v", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode: got %o, want 700", info.Mode().Perm())
	}
	if _, err := os.Lstat(paths.HostRunnerSocket); !os.IsNotExist(err) {
		t.Fatalf("runner socket must not be created: %v", err)
	}
}

// TestEnsureRuntimeDirectoryIdempotent 验证重复调用返回相同路径并收敛权限。
func TestEnsureRuntimeDirectoryIdempotent(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	first, err := EnsureRuntimeDirectory(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := os.Chmod(first.Directory, 0o755); err != nil {
		t.Fatalf("widen test directory mode: %v", err)
	}

	second, err := EnsureRuntimeDirectory(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.CreatedByThisCall {
		t.Fatal("repeated ensure reported a reused directory as newly created")
	}
	first.CreatedByThisCall = false
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("path identity changed: first=%#v second=%#v", first, second)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(second.Directory)
		if err != nil {
			t.Fatalf("inspect repeated directory: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("reconciled mode: got %o, want 700", info.Mode().Perm())
		}
	}
}

// TestEnsureRuntimeDirectoryRejectsSymlink 验证 sandbox 目录不能重定向到外部。
func TestEnsureRuntimeDirectoryRejectsSymlink(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	names, err := NamesForSandbox(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("generate names: %v", err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, names.RuntimeDirectory); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if _, err := EnsureRuntimeDirectory(
		dataDirectory,
		testSandboxID,
	); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

// TestEnsureRuntimeDirectoryRejectsUnsafeInputs 验证越界 ID 和未准备根目录。
func TestEnsureRuntimeDirectoryRejectsUnsafeInputs(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	tests := []struct {
		name string
		root string
		id   string
	}{
		{name: "path traversal", root: dataDirectory, id: "../sandbox"},
		{name: "separator", root: dataDirectory, id: testSandboxID + "/other"},
		{
			name: "missing run root",
			root: filepath.Join(t.TempDir(), "missing"),
			id:   testSandboxID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := EnsureRuntimeDirectory(tt.root, tt.id); err == nil {
				t.Fatal("expected unsafe input rejection")
			}
		})
	}
}
