//go:build linux

package runnerfiles

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestService 在临时目录上打开文件服务。
func newTestService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	service, err := Open(root)
	if err != nil {
		t.Fatalf("open files service: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

// TestFilesEngineRoundTrip 验收完整文件工作流：mkdir → upload → stat →
// list → download → move → delete，并验证二进制内容逐字节一致。
func TestFilesEngineRoundTrip(t *testing.T) {
	service := newTestService(t)

	created, stat, err := service.Mkdir("src/generated", true)
	if err != nil || !created || stat.Type != "directory" {
		t.Fatalf("mkdir parents: created=%v stat=%+v err=%v", created, stat, err)
	}

	payload := make([]byte, 4096)
	for index := range payload {
		payload[index] = byte(index % 251)
	}
	replaced, stat, err := service.Upload("src/generated/app.bin", bytes.NewReader(payload), false, false, 1<<20)
	if err != nil || replaced || stat.SizeBytes != int64(len(payload)) || stat.Type != "regular" {
		t.Fatalf("upload: replaced=%v stat=%+v err=%v", replaced, stat, err)
	}
	if stat.Mode != "0644" {
		t.Fatalf("upload mode: got %s want 0644", stat.Mode)
	}

	// 同一路径非覆盖上传必须冲突，且原内容不变。
	if _, _, err := service.Upload("src/generated/app.bin", bytes.NewReader([]byte("x")), false, false, 1<<20); !errors.Is(err, ErrConflict) {
		t.Fatalf("upload overwrite=false should conflict, got %v", err)
	}

	downloaded, err := service.Download("src/generated/app.bin", 1<<20)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(downloaded.Reader); err != nil {
		t.Fatalf("read download: %v", err)
	}
	_ = downloaded.Reader.Close()
	if !bytes.Equal(buffer.Bytes(), payload) {
		t.Fatalf("download content mismatch: got %d bytes want %d", buffer.Len(), len(payload))
	}

	stat, err = service.Stat("src/generated/app.bin")
	if err != nil || stat.SizeBytes != int64(len(payload)) {
		t.Fatalf("stat: %+v err=%v", stat, err)
	}

	listing, err := service.List("src")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Path != "src/generated" ||
		listing.Entries[0].Type != "directory" {
		t.Fatalf("list entries: %+v", listing.Entries)
	}

	if _, _, err := service.Mkdir("bin", true); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if _, err := service.Move("src/generated/app.bin", "bin/app.bin", true); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := service.Stat("src/generated/app.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("moved source should be gone, got %v", err)
	}
	moved, err := service.Download("bin/app.bin", 1<<20)
	if err != nil {
		t.Fatalf("download moved: %v", err)
	}
	buffer.Reset()
	if _, err := buffer.ReadFrom(moved.Reader); err != nil {
		t.Fatalf("read moved: %v", err)
	}
	_ = moved.Reader.Close()
	if !bytes.Equal(buffer.Bytes(), payload) {
		t.Fatal("moved content mismatch")
	}

	if err := service.Delete("bin", true); err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	if _, err := service.Stat("bin/app.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted path should be gone, got %v", err)
	}
	if err := service.Delete("bin/app.bin", false); err != nil {
		t.Fatalf("delete missing path should be idempotent: %v", err)
	}
}

// TestFilesEngineRejectsEscape 验证路径逃逸在协议层和内核层都被拒绝。
func TestFilesEngineRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	service, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer service.Close()

	for _, path := range []string{"../outside-secret.txt", "/etc/passwd", "a/../../outside-secret.txt", ""} {
		if _, _, err := service.Upload(path, bytes.NewReader([]byte("x")), false, false, 1<<20); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("upload %q should be invalid, got %v", path, err)
		}
	}

	// symlink 指向 workspace 外：跟随解析必须被 RESOLVE_BENEATH 拒绝。
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := service.Stat("leak"); !errors.Is(err, ErrNotFound) && err == nil {
		t.Fatalf("stat through escaping symlink should fail, got %+v", err)
	}
	if _, err := service.Stat("leak"); err == nil {
		t.Fatal("stat through escaping symlink must not succeed")
	}

	// 内部 symlink 指向 workspace 内文件：允许跟随。
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write real: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "inside")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	stat, err := service.Stat("inside")
	if err != nil || stat.Type != "regular" || stat.SizeBytes != 2 {
		t.Fatalf("stat inside symlink: %+v err=%v", stat, err)
	}
}

// TestFilesEngineLimits 验证上传与下载上限。
func TestFilesEngineLimits(t *testing.T) {
	service := newTestService(t)
	if _, _, err := service.Upload("big.bin", bytes.NewReader(make([]byte, 1024)), false, true, 512); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("upload over limit should fail, got %v", err)
	}
	if _, err := service.Stat("big.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("over-limit upload must not leave target, got %v", err)
	}
	entries, err := service.List(".")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	for _, entry := range entries.Entries {
		if bytes.HasPrefix([]byte(entry.Path), []byte(".minisandbox-upload-")) {
			t.Fatalf("temp file leaked: %s", entry.Path)
		}
	}
	if _, _, err := service.Upload("big.bin", bytes.NewReader(make([]byte, 128)), false, true, 512); err != nil {
		t.Fatalf("upload within limit: %v", err)
	}
	if _, err := service.Download("big.bin", 64); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("download over limit should fail, got %v", err)
	}
}

// TestFilesEngineDirectoryRules 验证目录删除与根保护。
func TestFilesEngineDirectoryRules(t *testing.T) {
	service := newTestService(t)
	if _, _, err := service.Mkdir("nested/deep", true); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := service.Delete("nested", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-empty directory delete should conflict, got %v", err)
	}
	if err := service.Delete(".", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("root delete should be invalid, got %v", err)
	}
	if _, _, err := service.Mkdir(".", true); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("root mkdir should be invalid, got %v", err)
	}
	if err := service.Delete("nested", true); err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	listing, err := service.List(".")
	if err != nil || len(listing.Entries) != 0 {
		t.Fatalf("root should be empty after delete: %+v err=%v", listing, err)
	}
}
