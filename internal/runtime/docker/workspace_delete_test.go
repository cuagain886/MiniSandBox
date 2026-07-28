package docker

import (
	"os"
	"path/filepath"
	"testing"
)

const otherTestSandboxID = "10010203-0405-4607-8809-0a0b0c0d0e0f"

// TestDeleteRuntimeDirectoryRemovesOnlyTarget 验证目标内容被删除且其他 sandbox 保留。
func TestDeleteRuntimeDirectoryRemovesOnlyTarget(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	target, err := EnsureRuntimeDirectory(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("ensure target directory: %v", err)
	}
	other, err := EnsureRuntimeDirectory(dataDirectory, otherTestSandboxID)
	if err != nil {
		t.Fatalf("ensure other directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(target.Directory, "runner.sock"),
		[]byte("stale"),
		0o600,
	); err != nil {
		t.Fatalf("write target content: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(other.Directory, "keep"),
		[]byte("other"),
		0o600,
	); err != nil {
		t.Fatalf("write other content: %v", err)
	}

	if err := DeleteRuntimeDirectory(
		dataDirectory,
		testSandboxID,
	); err != nil {
		t.Fatalf("delete runtime directory: %v", err)
	}
	if _, err := os.Lstat(target.Directory); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(other.Directory, "keep")); err != nil {
		t.Fatalf("other sandbox was affected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDirectory, "run")); err != nil {
		t.Fatalf("run root was removed: %v", err)
	}
}

// TestDeleteRuntimeDirectoryMissingIsIdempotent 验证目标或整个 run root 缺失均成功。
func TestDeleteRuntimeDirectoryMissingIsIdempotent(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	if err := DeleteRuntimeDirectory(
		dataDirectory,
		testSandboxID,
	); err != nil {
		t.Fatalf("delete missing target: %v", err)
	}

	missingDataDirectory := filepath.Join(t.TempDir(), "missing-data")
	if err := DeleteRuntimeDirectory(
		missingDataDirectory,
		testSandboxID,
	); err != nil {
		t.Fatalf("delete below missing root: %v", err)
	}
}

// TestDeleteRuntimeDirectoryRejectsSymlink 验证 symlink 本身和外部目标都不会删除。
func TestDeleteRuntimeDirectoryRejectsSymlink(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	names, err := NamesForSandbox(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("build names: %v", err)
	}
	external := t.TempDir()
	externalFile := filepath.Join(external, "keep")
	if err := os.WriteFile(externalFile, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(external, names.RuntimeDirectory); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if err := DeleteRuntimeDirectory(
		dataDirectory,
		testSandboxID,
	); err == nil {
		t.Fatal("symlink runtime directory was accepted")
	}
	if _, err := os.Lstat(externalFile); err != nil {
		t.Fatalf("symlink target was affected: %v", err)
	}
}

// TestDeleteRuntimeDirectoryRejectsUnsafeTarget 验证越界 ID 和 run root symlink。
func TestDeleteRuntimeDirectoryRejectsUnsafeTarget(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	for _, sandboxID := range []string{
		"../run",
		testSandboxID + "/other",
		"",
	} {
		if err := DeleteRuntimeDirectory(
			dataDirectory,
			sandboxID,
		); err == nil {
			t.Fatalf("unsafe sandbox ID accepted: %q", sandboxID)
		}
	}

	symlinkDataDirectory := filepath.Join(t.TempDir(), "data")
	externalRunRoot := t.TempDir()
	if err := os.Mkdir(symlinkDataDirectory, 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.Symlink(
		externalRunRoot,
		filepath.Join(symlinkDataDirectory, "run"),
	); err != nil {
		t.Skipf("run root symlink unavailable: %v", err)
	}
	if err := DeleteRuntimeDirectory(
		symlinkDataDirectory,
		testSandboxID,
	); err == nil {
		t.Fatal("symlink run root was accepted")
	}
}
