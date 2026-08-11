package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const leaseTestID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

func leaseTestManifest() LeaseManifest {
	return LeaseManifest{
		SchemaVersion: LeaseManifestSchemaVersion, SandboxID: leaseTestID,
		SpecHash: strings.Repeat("a", 64), ExpiresAt: time.Date(2028, 9, 10, 11, 12, 13, 0, time.UTC),
		ProjectedStoreRevision: 7,
	}
}

// TestLeaseManifestRoundTripAndAllowlist 验证稳定格式只包含冻结的五个字段。
func TestLeaseManifestRoundTripAndAllowlist(t *testing.T) {
	encoded, err := EncodeLeaseManifest(leaseTestManifest())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, forbidden := range []string{"token", "command", "env", "host_path", "socket"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("manifest leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
	decoded, err := DecodeLeaseManifest(encoded)
	if err != nil || decoded != leaseTestManifest() {
		t.Fatalf("round trip: %#v/%v", decoded, err)
	}
	unknown := []byte(strings.TrimSuffix(string(encoded), "\n}") + `,"token":"secret"}`)
	if _, err := DecodeLeaseManifest(unknown); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

// TestLeaseManifestRejectsInvalidVersionFieldsAndSize 验证版本、身份、revision 和大小边界。
func TestLeaseManifestRejectsInvalidVersionFieldsAndSize(t *testing.T) {
	for _, mutate := range []func(*LeaseManifest){
		func(m *LeaseManifest) { m.SchemaVersion = 2 },
		func(m *LeaseManifest) { m.SandboxID = "unsafe" },
		func(m *LeaseManifest) { m.SpecHash = "secret" },
		func(m *LeaseManifest) { m.ExpiresAt = time.Time{} },
		func(m *LeaseManifest) { m.ProjectedStoreRevision = 0 },
	} {
		manifest := leaseTestManifest()
		mutate(&manifest)
		if _, err := EncodeLeaseManifest(manifest); err == nil {
			t.Fatalf("invalid manifest accepted: %#v", manifest)
		}
	}
	if _, err := DecodeLeaseManifest(make([]byte, MaxLeaseManifestBytes+1)); err == nil {
		t.Fatal("oversized manifest accepted")
	}
}

// TestLeaseManifestWriterAtomicallyReplacesRegularFile 验证重复写只有完整旧版或新版。
func TestLeaseManifestWriterAtomicallyReplacesRegularFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, leaseTestID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := NewLeaseManifestWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	first := leaseTestManifest()
	if err := writer.Write(first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := first
	second.ProjectedStoreRevision++
	second.ExpiresAt = second.ExpiresAt.Add(time.Hour)
	if err := writer.Write(second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(directory, LeaseManifestName))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLeaseManifest(content)
	if err != nil || decoded != second {
		t.Fatalf("final manifest: %#v/%v", decoded, err)
	}
	if info, _ := os.Stat(filepath.Join(directory, LeaseManifestName)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: got %o, want 600", info.Mode().Perm())
	}
}

// TestLeaseManifestWriterPreservesOldFileOnRenameFailure 验证失败不会暴露半 JSON。
func TestLeaseManifestWriterPreservesOldFileOnRenameFailure(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, leaseTestID)
	_ = os.Mkdir(directory, 0o700)
	writer, _ := NewLeaseManifestWriter(root)
	old := []byte("old-complete")
	target := filepath.Join(directory, LeaseManifestName)
	_ = os.WriteFile(target, old, 0o600)
	injected := errors.New("rename failed")
	writer.rename = func(string, string) error { return injected }
	if err := writer.Write(leaseTestManifest()); !errors.Is(err, injected) {
		t.Fatalf("rename failure: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(old) {
		t.Fatalf("old file changed: %q", got)
	}
}

// TestLeaseManifestWriterReportsDirectorySyncFailureAfterCompleteRename 验证 fsync 故障不产生半文件。
func TestLeaseManifestWriterReportsDirectorySyncFailureAfterCompleteRename(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, leaseTestID)
	_ = os.Mkdir(directory, 0o700)
	writer, _ := NewLeaseManifestWriter(root)
	injected := errors.New("directory sync failed")
	writer.syncDir = func(string) error { return injected }
	if err := writer.Write(leaseTestManifest()); !errors.Is(err, injected) {
		t.Fatalf("sync failure: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(directory, LeaseManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLeaseManifest(content); err != nil {
		t.Fatalf("sync failure exposed partial file: %v", err)
	}
}

// TestLeaseManifestWriterRejectsSymlinkTarget 验证固定文件不能被 symlink 劫持。
func TestLeaseManifestWriterRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, leaseTestID)
	_ = os.Mkdir(directory, 0o700)
	target := filepath.Join(root, "outside")
	_ = os.WriteFile(target, []byte("outside"), 0o600)
	if err := os.Symlink(target, filepath.Join(directory, LeaseManifestName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writer, _ := NewLeaseManifestWriter(root)
	if err := writer.Write(leaseTestManifest()); err == nil {
		t.Fatal("symlink target accepted")
	}
}
