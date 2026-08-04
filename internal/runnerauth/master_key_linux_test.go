//go:build linux

package runnerauth

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minisandbox/internal/config"
)

func writeMasterKeyFixture(t *testing.T, content []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner-master-key")
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("write master key fixture: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod master key fixture: %v", err)
	}
	return path
}

// TestLoadMasterKeySuccess 验证 0400/0600 regular file 均可读取，返回内容
// 精确且 Clear 能清零调用方持有的实例。
func TestLoadMasterKeySuccess(t *testing.T) {
	want := bytes.Repeat([]byte{0x5a}, masterKeyBytes)
	for _, mode := range []os.FileMode{0o400, 0o600} {
		path := writeMasterKeyFixture(t, want, mode)
		got, err := LoadMasterKey(path)
		if err != nil {
			t.Fatalf("load mode %o: %v", mode, err)
		}
		if !bytes.Equal(got[:], want) {
			t.Fatal("loaded master key differs")
		}
		got.Clear()
		if !allZero(got[:]) {
			t.Fatal("master key Clear did not zero the instance")
		}
	}
}

// TestLoadMasterKeyRejectsUnsafeFiles 验证空、短、超长、全零、宽权限、symlink
// 与相对路径均失败，且错误不泄露路径或内容 canary。
func TestLoadMasterKeyRejectsUnsafeFiles(t *testing.T) {
	canary := "master-key-canary-must-not-leak"
	valid := bytes.Repeat([]byte{0x41}, masterKeyBytes)
	tests := []struct {
		name string
		path func(*testing.T) string
	}{
		{"empty", func(t *testing.T) string { return writeMasterKeyFixture(t, nil, 0o600) }},
		{"short", func(t *testing.T) string { return writeMasterKeyFixture(t, []byte(canary), 0o600) }},
		{"long", func(t *testing.T) string { return writeMasterKeyFixture(t, append(valid, 'x'), 0o600) }},
		{"all zero", func(t *testing.T) string { return writeMasterKeyFixture(t, make([]byte, masterKeyBytes), 0o600) }},
		{"wide mode", func(t *testing.T) string { return writeMasterKeyFixture(t, valid, 0o644) }},
		{"relative", func(*testing.T) string { return canary }},
		{"symlink", func(t *testing.T) string {
			target := writeMasterKeyFixture(t, valid, 0o600)
			link := filepath.Join(t.TempDir(), canary)
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("create key symlink: %v", err)
			}
			return link
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path(t)
			_, err := LoadMasterKey(path)
			if err == nil {
				t.Fatal("unsafe master key accepted")
			}
			if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked key path or content: %v", err)
			}
		})
	}
}

// TestMasterKeyDoesNotEnterConfigDump 验证普通 resolved config 只保存 secret
// 文件路径，不会因加载操作包含主密钥字节。
func TestMasterKeyDoesNotEnterConfigDump(t *testing.T) {
	content := bytes.Repeat([]byte{0xa5}, masterKeyBytes)
	path := writeMasterKeyFixture(t, content, 0o600)
	key, err := LoadMasterKey(path)
	if err != nil {
		t.Fatalf("load master key: %v", err)
	}
	defer key.Clear()
	cfg := config.Default()
	cfg.Security.RunnerMasterKeyFile = path
	dump := fmt.Sprintf("%+v", cfg)
	if strings.Contains(dump, fmt.Sprintf("%x", content)) || strings.Contains(dump, string(content)) {
		t.Fatal("master key entered ordinary config dump")
	}
}
