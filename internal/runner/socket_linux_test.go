//go:build linux

package runner

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
)

func socketFixture(t *testing.T) (string, string, runnerbootstrap.Identity) {
	t.Helper()
	runtimeDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runtimeDirectory, 0o755); err != nil {
		t.Fatalf("create runtime directory: %v", err)
	}
	return runtimeDirectory, filepath.Join(runtimeDirectory, "runner.sock"), runnerbootstrap.Identity{
		ExecutionUID:   uint32(os.Getuid()) + 1,
		ExecutionGID:   uint32(os.Getgid()) + 1,
		SocketOwnerUID: uint32(os.Getuid()),
		SocketOwnerGID: uint32(os.Getgid()),
	}
}

// TestBindManagedSocketOwnerModeAndAccept 验证 owner/mode 收敛后 listener fd
// 可正常 accept，且 pathname 没有 execution 用户可用的 group/other 权限。
func TestBindManagedSocketOwnerModeAndAccept(t *testing.T) {
	directory, socketPath, identity := socketFixture(t)
	listener, err := bindManagedSocketAt(directory, socketPath, identity)
	if err != nil {
		t.Fatalf("bind managed socket: %v", err)
	}
	defer listener.Close()

	for _, expected := range []struct {
		path string
		mode os.FileMode
		kind os.FileMode
	}{{directory, 0o700, os.ModeDir}, {socketPath, 0o600, os.ModeSocket}} {
		if err := verifyManagedPath(expected.path, identity.SocketOwnerUID, identity.SocketOwnerGID, expected.mode, expected.kind); err != nil {
			t.Fatalf("verify %s: %v", expected.path, err)
		}
	}
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		accepted <- err
	}()
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("dial managed socket: %v", err)
	}
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("accept managed socket: %v", err)
	}
}

// TestBindManagedSocketRejectsIdentityCollision 验证 UID 或 GID 任一与 socket
// owner 相同都会在文件系统副作用前失败。
func TestBindManagedSocketRejectsIdentityCollision(t *testing.T) {
	for _, field := range []string{"uid", "gid"} {
		t.Run(field, func(t *testing.T) {
			directory, socketPath, identity := socketFixture(t)
			if field == "uid" {
				identity.ExecutionUID = identity.SocketOwnerUID
			} else {
				identity.ExecutionGID = identity.SocketOwnerGID
			}
			if _, err := bindManagedSocketAt(directory, socketPath, identity); err == nil {
				t.Fatal("identity collision accepted")
			}
			if info, err := os.Lstat(directory); err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("directory changed before identity rejection: %v %v", info, err)
			}
		})
	}
}

// TestBindManagedSocketReplacesOnlyStaleSocket 验证真实 stale socket 可替换，
// symlink 与普通文件占位均拒绝且不删除目标。
func TestBindManagedSocketReplacesOnlyStaleSocket(t *testing.T) {
	t.Run("stale socket", func(t *testing.T) {
		directory, socketPath, identity := socketFixture(t)
		stale, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatalf("create stale socket: %v", err)
		}
		stale.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = stale.Close()
		listener, err := bindManagedSocketAt(directory, socketPath, identity)
		if err != nil {
			t.Fatalf("replace stale socket: %v", err)
		}
		_ = listener.Close()
	})
	for _, kind := range []string{"symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			directory, socketPath, identity := socketFixture(t)
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
					t.Fatalf("create target: %v", err)
				}
				if err := os.Symlink(target, socketPath); err != nil {
					t.Fatalf("create socket symlink: %v", err)
				}
			} else if err := os.WriteFile(socketPath, []byte("sentinel"), 0o600); err != nil {
				t.Fatalf("create socket file collision: %v", err)
			}
			if _, err := bindManagedSocketAt(directory, socketPath, identity); err == nil {
				t.Fatalf("%s collision accepted", kind)
			}
			if _, err := os.Lstat(socketPath); err != nil {
				t.Fatalf("collision was deleted: %v", err)
			}
		})
	}
}

// TestBindManagedSocketRejectsRuntimeSymlink 验证 runtime parent 不能借 symlink
// 把 root chown/chmod 和 socket 创建引导到其他目录。
func TestBindManagedSocketRejectsRuntimeSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	directory := filepath.Join(root, "run")
	if err := os.Symlink(target, directory); err != nil {
		t.Fatalf("create runtime symlink: %v", err)
	}
	identity := runnerbootstrap.Identity{ExecutionUID: uint32(syscall.Getuid()) + 1, ExecutionGID: uint32(syscall.Getgid()) + 1, SocketOwnerUID: uint32(syscall.Getuid()), SocketOwnerGID: uint32(syscall.Getgid())}
	if _, err := bindManagedSocketAt(directory, filepath.Join(directory, "runner.sock"), identity); err == nil {
		t.Fatal("runtime symlink accepted")
	}
}
