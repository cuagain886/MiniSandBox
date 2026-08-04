//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"minisandbox/internal/runnerbootstrap"
)

func managedDirectoryFixture(t *testing.T) (managedDirectoryPaths, uint32, uint32) {
	t.Helper()
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "runtime")
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatalf("create runtime directory: %v", err)
	}
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return managedDirectoryPaths{
		executionData: filepath.Join(runtimeDirectory, "executions"),
		workspace:     workspace,
	}, uint32(os.Getuid()), uint32(os.Getgid())
}

// TestInitializeManagedDirectoriesCreatesAndRepeats 验证首次创建、owner/mode
// 收敛和重复 bootstrap 幂等，并证明 chown 只作用于两个目录根。
func TestInitializeManagedDirectoriesCreatesAndRepeats(t *testing.T) {
	paths, uid, gid := managedDirectoryFixture(t)
	child := filepath.Join(paths.workspace, "user-file")
	if err := os.WriteFile(child, []byte("user"), 0o640); err != nil {
		t.Fatalf("create workspace child: %v", err)
	}
	before, err := os.Lstat(child)
	if err != nil {
		t.Fatalf("stat child before bootstrap: %v", err)
	}

	var chowned []string
	ops := osDirectoryOps
	ops.chown = func(path string, ownerUID, ownerGID int) error {
		chowned = append(chowned, path)
		return os.Chown(path, ownerUID, ownerGID)
	}
	for range 2 {
		if err := initializeManagedDirectories(paths, uid, gid, ops); err != nil {
			t.Fatalf("initialize managed directories: %v", err)
		}
	}
	wantChowned := []string{paths.executionData, paths.workspace, paths.executionData, paths.workspace}
	if !reflect.DeepEqual(chowned, wantChowned) {
		t.Fatalf("chown targets: got %v, want %v", chowned, wantChowned)
	}
	for _, path := range []string{paths.executionData, paths.workspace} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat managed directory %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("managed directory %s mode: got %v, want 0700", path, info.Mode())
		}
	}
	after, err := os.Lstat(child)
	if err != nil {
		t.Fatalf("stat child after bootstrap: %v", err)
	}
	if before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("workspace child metadata changed recursively")
	}
}

// TestInitializeManagedDirectoriesRejectsSymlinks 验证 execution data 与
// workspace 任一目标为 symlink 都会 fail closed。
func TestInitializeManagedDirectoriesRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"runtime-parent", "execution-data", "workspace"} {
		t.Run(target, func(t *testing.T) {
			paths, uid, gid := managedDirectoryFixture(t)
			realDirectory := filepath.Join(t.TempDir(), "real")
			if err := os.Mkdir(realDirectory, 0o700); err != nil {
				t.Fatalf("create symlink target: %v", err)
			}
			path := paths.executionData
			if target == "runtime-parent" {
				path = filepath.Dir(paths.executionData)
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove runtime parent: %v", err)
				}
			} else if target == "workspace" {
				path = paths.workspace
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove workspace: %v", err)
				}
			}
			if err := os.Symlink(realDirectory, path); err != nil {
				t.Fatalf("create symlink: %v", err)
			}
			if err := initializeManagedDirectories(paths, uid, gid, osDirectoryOps); err == nil {
				t.Fatal("symlink managed path accepted")
			}
		})
	}
}

// TestInitializeManagedDirectoriesRejectsConfiguredPaths 验证生产入口只接受
// P2-008 固定路径，不能把任意 bootstrap/request 路径带入 root 文件操作。
func TestInitializeManagedDirectoriesRejectsConfiguredPaths(t *testing.T) {
	bootstrap := runnerbootstrap.Config{
		Paths: runnerbootstrap.Paths{
			ExecutionDataDirectory: "/tmp/attacker",
			WorkspaceDirectory:     "/workspace",
			RuntimeDirectory:       runnerbootstrap.RuntimeDirectory,
			SocketPath:             runnerbootstrap.SocketPath,
		},
		Identity: runnerbootstrap.Identity{ExecutionUID: 1000, ExecutionGID: 1000},
	}
	if err := InitializeManagedDirectories(bootstrap); err == nil {
		t.Fatal("caller-provided managed path accepted")
	}
}

// TestInitializeManagedDirectoriesVerifiesOwner 验证 chown 返回成功但 owner
// 未实际收敛时仍会在回验阶段失败。
func TestInitializeManagedDirectoriesVerifiesOwner(t *testing.T) {
	paths, uid, gid := managedDirectoryFixture(t)
	ops := osDirectoryOps
	ops.chown = func(string, int, int) error { return nil }
	if err := initializeManagedDirectories(paths, uid+1, gid+1, ops); err == nil {
		t.Fatal("wrong owner accepted after no-op chown")
	}
}

// TestInitializeManagedDirectoriesRequiresWorkspace 验证缺失 mount root 不会被
// bootstrap 在镜像层悄悄创建。
func TestInitializeManagedDirectoriesRequiresWorkspace(t *testing.T) {
	paths, uid, gid := managedDirectoryFixture(t)
	if err := os.Remove(paths.workspace); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if err := initializeManagedDirectories(paths, uid, gid, osDirectoryOps); err == nil {
		t.Fatal("missing workspace mount root accepted")
	}
	if _, err := os.Lstat(paths.workspace); !os.IsNotExist(err) {
		t.Fatalf("missing workspace was created: %v", err)
	}
}
