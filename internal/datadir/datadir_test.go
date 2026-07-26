package datadir

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// testInputs 基于临时根目录构造一组合法输入。
func testInputs(t *testing.T) (string, string, string) {
	t.Helper()

	root := t.TempDir()
	dataDirectory := filepath.Join(root, "data")
	databasePath := filepath.Join(root, "data", "db", "sandboxd.db")
	runRoot := filepath.Join(root, "data", "run")
	return dataDirectory, databasePath, runRoot
}

// TestEnsureCreatesDirectories 验证首次调用创建全部受管目录并返回确定路径。
func TestEnsureCreatesDirectories(t *testing.T) {
	dataDirectory, databasePath, runRoot := testInputs(t)

	paths, err := Ensure(dataDirectory, databasePath, runRoot)
	if err != nil {
		t.Fatalf("ensure managed directories: %v", err)
	}

	want := Paths{
		DataDirectory: dataDirectory,
		DatabasePath:  databasePath,
		RunRoot:       runRoot,
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("unexpected paths:\ngot  %+v\nwant %+v", paths, want)
	}

	for _, directory := range []string{
		dataDirectory,
		filepath.Dir(databasePath),
		runRoot,
	} {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("managed directory missing: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("managed path is not a directory: %s", directory)
		}
		if runtime.GOOS != "windows" {
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf(
					"unexpected directory mode for %s: got %o, want 700",
					directory,
					got,
				)
			}
		}
	}

	// 数据库文件本身不应被创建,本包只负责父目录。
	if _, err := os.Lstat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database file must not be created: %v", err)
	}
}

// TestEnsureIdempotent 验证重复调用成功且返回相同路径。
func TestEnsureIdempotent(t *testing.T) {
	dataDirectory, databasePath, runRoot := testInputs(t)

	first, err := Ensure(dataDirectory, databasePath, runRoot)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := Ensure(dataDirectory, databasePath, runRoot)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf(
			"repeated ensure changed paths:\nfirst  %+v\nsecond %+v",
			first,
			second,
		)
	}
}

// TestEnsureRejectsRelativePaths 验证任一相对路径输入都被拒绝。
func TestEnsureRejectsRelativePaths(t *testing.T) {
	dataDirectory, databasePath, runRoot := testInputs(t)

	tests := []struct {
		name string
		call func() (Paths, error)
	}{
		{
			name: "relative data directory",
			call: func() (Paths, error) {
				return Ensure("data", databasePath, runRoot)
			},
		},
		{
			name: "relative database path",
			call: func() (Paths, error) {
				return Ensure(dataDirectory, "sandboxd.db", runRoot)
			},
		},
		{
			name: "relative run root",
			call: func() (Paths, error) {
				return Ensure(dataDirectory, databasePath, "run")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.call(); err == nil {
				t.Fatal("expected error for relative path")
			}
		})
	}
}

// TestEnsureRejectsSymlinkDataDir 验证 symlink 数据目录被拒绝。
func TestEnsureRejectsSymlinkDataDir(t *testing.T) {
	dataDirectory, databasePath, runRoot := testInputs(t)

	realTarget := filepath.Join(t.TempDir(), "real-data")
	if err := os.MkdirAll(realTarget, 0o700); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	if err := os.Symlink(realTarget, dataDirectory); err != nil {
		// Windows 上创建 symlink 需要额外权限;该行为已在注释中约定,
		// 并需在 Linux 环境的测试运行中验证。
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if _, err := Ensure(dataDirectory, databasePath, runRoot); err == nil {
		t.Fatal("expected error for symlink data directory")
	}
}

// TestEnsureRejectsFileCollision 验证路径被普通文件占用时拒绝。
func TestEnsureRejectsFileCollision(t *testing.T) {
	dataDirectory, databasePath, runRoot := testInputs(t)

	if err := os.MkdirAll(filepath.Dir(dataDirectory), 0o700); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(dataDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("occupy path with file: %v", err)
	}

	if _, err := Ensure(dataDirectory, databasePath, runRoot); err == nil {
		t.Fatal("expected error for non-directory managed path")
	}
}
