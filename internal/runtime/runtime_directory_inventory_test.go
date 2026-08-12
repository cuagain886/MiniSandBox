package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestInventoryRuntimeDirectoriesReadsValidAndMissingManifest 验证仅读取固定 manifest，缺失是正常事实。
func TestInventoryRuntimeDirectoriesReadsValidAndMissingManifest(t *testing.T) {
	root := t.TempDir()
	withManifest := leaseTestID
	withoutManifest := "10010203-0405-4607-8809-0a0b0c0d0e0f"
	for _, id := range []string{withManifest, withoutManifest} {
		if err := os.Mkdir(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	content, _ := EncodeLeaseManifest(leaseTestManifest())
	if err := os.WriteFile(filepath.Join(root, withManifest, LeaseManifestName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, withManifest, "user-secret"), []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := InventoryRuntimeDirectories(root)
	if err != nil || len(got) != 2 {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	if got[0].Manifest == nil || got[0].Manifest.SandboxID != withManifest || got[1].ManifestPresent || got[1].DiscoveryIssue != "" {
		t.Fatalf("observations: %#v", got)
	}
}

// TestInventoryRuntimeDirectoriesRejectsUnsafeDirectoryAndManifestLinks 验证顶层目录和固定文件均不能由链接替代。
func TestInventoryRuntimeDirectoriesRejectsUnsafeDirectoryAndManifestLinks(t *testing.T) {
	root := t.TempDir()
	directoryID := leaseTestID
	fileID := "10010203-0405-4607-8809-0a0b0c0d0e0f"
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, directoryID)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, fileID), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(external, LeaseManifestName)
	_ = os.WriteFile(target, []byte("outside"), 0o600)
	if err := os.Symlink(target, filepath.Join(root, fileID, LeaseManifestName)); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	got, err := InventoryRuntimeDirectories(root)
	if err != nil || len(got) != 2 || got[0].DiscoveryIssue != DiscoveryDirectoryUnsafe || got[1].DiscoveryIssue != DiscoveryManifestUnsafe {
		t.Fatalf("unsafe inventory: %#v/%v", got, err)
	}
}

// TestInventoryRuntimeDirectoriesRejectsOversizedInvalidAndUnknownEntries 验证大小、JSON 和未知目录名均形成安全 anomaly。
func TestInventoryRuntimeDirectoriesRejectsOversizedInvalidAndUnknownEntries(t *testing.T) {
	root := t.TempDir()
	ids := []string{leaseTestID, "10010203-0405-4607-8809-0a0b0c0d0e0f"}
	for _, id := range ids {
		_ = os.Mkdir(filepath.Join(root, id), 0o700)
	}
	_ = os.Mkdir(filepath.Join(root, "unknown-secret-name"), 0o700)
	_ = os.WriteFile(filepath.Join(root, ids[0], LeaseManifestName), []byte("{"), 0o600)
	_ = os.WriteFile(filepath.Join(root, ids[1], LeaseManifestName), []byte(strings.Repeat("x", MaxLeaseManifestBytes+1)), 0o600)
	got, err := InventoryRuntimeDirectories(root)
	if err != nil || len(got) != 3 {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	issues := map[string]int{}
	for _, observation := range got {
		issues[observation.DiscoveryIssue]++
	}
	if issues[DiscoveryManifestInvalid] != 2 || issues[DiscoveryDirectoryNameInvalid] != 1 {
		t.Fatalf("issues: %#v", issues)
	}
}

// TestRuntimeDirectoryScannerReturnsSafePermissionErrors 验证根目录与单 manifest 权限故障都不泄漏绝对路径。
func TestRuntimeDirectoryScannerReturnsSafePermissionErrors(t *testing.T) {
	root := t.TempDir()
	scanner := runtimeDirectoryScanner{lstat: os.Lstat, readDir: func(string) ([]os.DirEntry, error) {
		return nil, errors.New("permission denied at " + root)
	}}
	_, err := scanner.inventory(root)
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("unsafe root error: %v", err)
	}

	directory := filepath.Join(root, leaseTestID)
	_ = os.Mkdir(directory, 0o700)
	manifest := filepath.Join(directory, LeaseManifestName)
	_ = os.WriteFile(manifest, []byte("{}"), 0o600)
	scanner = runtimeDirectoryScanner{lstat: os.Lstat, readDir: os.ReadDir, readManifest: func(string, os.FileInfo) ([]byte, error) {
		return nil, errors.New("permission denied at " + manifest)
	}}
	got, err := scanner.inventory(root)
	if err != nil || len(got) != 1 || got[0].DiscoveryIssue != DiscoveryManifestUnavailable {
		t.Fatalf("manifest permission: %#v/%v", got, err)
	}
}

// TestRuntimeDirectoryObservationDoesNotExposeHostPath 锁定 observation 不携带绝对宿主机路径。
func TestRuntimeDirectoryObservationDoesNotExposeHostPath(t *testing.T) {
	typeName := reflect.TypeOf(RuntimeDirectoryObservation{})
	for _, forbidden := range []string{"Path", "DirectoryPath", "ManifestPath", "HostPath"} {
		if _, ok := typeName.FieldByName(forbidden); ok {
			t.Fatalf("observation exposes %s", forbidden)
		}
	}
}
