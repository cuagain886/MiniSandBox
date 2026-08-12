//go:build integration

package testcrashpoint

import (
	"bufio"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestHitNotifiesAndBlocksUntilControllerRelease 验证 IPC 先确认命中且不会把普通返回冒充进程崩溃。
func TestHitNotifiesAndBlocksUntilControllerRelease(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "crashpoint.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv(crashpointSocketEnv, socket)
	t.Setenv(crashpointNameEnv, "self-check")

	done := make(chan struct{})
	go func() {
		Hit("self-check")
		close(done)
	}()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || line != "self-check\n" {
		t.Fatalf("notification=%q err=%v", line, err)
	}
	select {
	case <-done:
		t.Fatal("crashpoint returned before controller release")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := connection.Write([]byte{'\n'}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("crashpoint did not return after controller release")
	}
}

// TestDropConsumesOnlyOneExactMatch 验证一次性丢弃不会影响其他事件或后续轮次。
func TestDropConsumesOnlyOneExactMatch(t *testing.T) {
	t.Setenv(dropPointNameEnv, "wake.create")
	if Drop("wake.delete") || !Drop("wake.create") || Drop("wake.create") {
		t.Fatal("drop point did not consume exactly one matching event")
	}
}
