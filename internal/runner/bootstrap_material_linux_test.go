//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/config"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// TestWaitLoadBootstrapMaterialHandlesDelayedBindVisibility 验证仅缺失材料会被短暂重试，
// 两个文件可见后仍通过原有 owner、mode、内容校验并完成一次性消费。
func TestWaitLoadBootstrapMaterialHandlesDelayedBindVisibility(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bootstrap ownership acquisition requires the container root capability model")
	}
	directory, err := os.MkdirTemp("/tmp", "minisandbox-bootstrap-wait-")
	if err != nil {
		t.Fatalf("create runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	control := config.Default()
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if control.Runner.ExecutionUID == uid {
		control.Runner.ExecutionUID++
	}
	if control.Runner.ExecutionGID == gid {
		control.Runner.ExecutionGID++
	}
	const sandboxID = "018f1111-2222-4333-8444-555555555555"
	value, err := runnerbootstrap.FromConfig(control, sandboxID, uid, gid)
	if err != nil {
		t.Fatalf("build bootstrap: %v", err)
	}
	encoded, err := runnerbootstrap.Marshal(value)
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	var key runnerauth.MasterKey
	for index := range key {
		key[index] = byte(index + 1)
	}
	token, err := runnerauth.DeriveToken(&key, sandboxID)
	key.Clear()
	if err != nil {
		t.Fatalf("derive token: %v", err)
	}
	go func() {
		time.Sleep(75 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(directory, runnerbootstrap.ConfigFileName), encoded, 0o600)
		_ = runnerauth.StageTokenFile(directory, uid, gid, &token)
	}()
	loaded, credential, err := WaitLoadBootstrapMaterial(directory, time.Second)
	if err != nil {
		t.Fatalf("wait load bootstrap: %v", err)
	}
	defer credential.Clear()
	if loaded.SandboxID != sandboxID {
		t.Fatalf("sandbox ID: got %q", loaded.SandboxID)
	}
	for _, name := range []string{runnerbootstrap.ConfigFileName, runnerauth.CredentialFileName} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("consumed material remains: %s: %v", name, err)
		}
	}
}
