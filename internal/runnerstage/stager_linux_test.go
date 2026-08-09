//go:build linux

package runnerstage

import (
	"os"
	"path/filepath"
	"testing"

	"minisandbox/internal/config"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// TestStagerPublishesConfigAndToken 验证受管目录内只出现固定配置与一次性凭据文件。
func TestStagerPublishesConfigAndToken(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "minisandbox-runnerstage-")
	if err != nil {
		t.Fatalf("create native Linux temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	keyDirectory, err := os.MkdirTemp("/tmp", "minisandbox-runnerkey-")
	if err != nil {
		t.Fatalf("create key directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(keyDirectory) })
	keyPath := filepath.Join(keyDirectory, "master-key")
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	if err := os.WriteFile(keyPath, key, 0o400); err != nil {
		t.Fatalf("write master key: %v", err)
	}
	control := config.Default()
	control.Security.RunnerMasterKeyFile = keyPath
	// 测试进程可能由普通 UID 运行，必须显式选择不同的 execution identity。
	if uint32(os.Geteuid()) == control.Runner.ExecutionUID {
		control.Runner.ExecutionUID++
	}
	if uint32(os.Getegid()) == control.Runner.ExecutionGID {
		control.Runner.ExecutionGID++
	}
	stager, err := New(control)
	if err != nil {
		t.Fatalf("new stager: %v", err)
	}
	defer stager.Close()
	const sandboxID = "018f1111-2222-4333-8444-555555555555"
	if err := stager.Stage(directory, sandboxID); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, runnerbootstrap.ConfigFileName)); err != nil {
		t.Fatalf("bootstrap config missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, runnerauth.CredentialFileName)); err != nil {
		t.Fatalf("runner token missing: %v", err)
	}
}
