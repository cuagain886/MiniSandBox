//go:build linux

package runnerauth

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func credentialDirectoryFixture(t *testing.T) (string, uint32, uint32) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create credential directory: %v", err)
	}
	return directory, uint32(os.Getuid()), uint32(os.Getgid())
}

func derivedTokenFixture(t *testing.T, id string) Token {
	t.Helper()
	var key MasterKey
	for index := range key {
		key[index] = byte(index + 1)
	}
	token, err := DeriveToken(&key, id)
	key.Clear()
	if err != nil {
		t.Fatalf("derive token fixture: %v", err)
	}
	return token
}

// TestStageAndConsumeTokenFile 验证 0600/owner、暂存方清零、消费后 unlink，
// 且返回 token 与原派生值精确一致。
func TestStageAndConsumeTokenFile(t *testing.T) {
	directory, uid, gid := credentialDirectoryFixture(t)
	token := derivedTokenFixture(t, testSandboxID)
	want := token
	defer want.Clear()
	if err := StageTokenFile(directory, uid, gid, &token); err != nil {
		t.Fatalf("stage token: %v", err)
	}
	if !allZero(token[:]) {
		t.Fatal("staging did not clear caller token")
	}
	path := filepath.Join(directory, CredentialFileName)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat staged token: %v", err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uid || stat.Gid != gid || info.Size() != tokenBytes {
		t.Fatalf("unsafe staged token metadata: mode=%v uid=%d gid=%d size=%d", info.Mode(), stat.Uid, stat.Gid, info.Size())
	}
	got, err := ConsumeTokenFile(directory, uid, gid)
	if err != nil {
		t.Fatalf("consume token: %v", err)
	}
	defer got.Clear()
	if got != want {
		t.Fatal("consumed token differs from derived token")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential remains after consumption: %v", err)
	}
}

// TestStageTokenFileAtomicallyReplacesSafeCredential 验证 sandboxd 重启可用
// 同一主密钥重新派生并安全覆盖未消费的 regular credential。
func TestStageTokenFileAtomicallyReplacesSafeCredential(t *testing.T) {
	directory, uid, gid := credentialDirectoryFixture(t)
	first := derivedTokenFixture(t, testSandboxID)
	if err := StageTokenFile(directory, uid, gid, &first); err != nil {
		t.Fatalf("stage first token: %v", err)
	}
	second := derivedTokenFixture(t, "10010203-0405-4607-8809-0a0b0c0d0e0f")
	want := second
	defer want.Clear()
	if err := StageTokenFile(directory, uid, gid, &second); err != nil {
		t.Fatalf("stage replacement token: %v", err)
	}
	got, err := ConsumeTokenFile(directory, uid, gid)
	if err != nil {
		t.Fatalf("consume replacement token: %v", err)
	}
	defer got.Clear()
	if got != want {
		t.Fatal("atomic replacement did not publish the latest token")
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".runner-token.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary credential files remain: %v %v", matches, err)
	}
}

// TestTokenFileRejectsSymlinksAndUnsafeMetadata 验证 runtime/credential symlink、
// 宽权限与 owner 不一致全部 fail closed，且 symlink 不会被删除或覆盖。
func TestTokenFileRejectsSymlinksAndUnsafeMetadata(t *testing.T) {
	t.Run("runtime symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("create target: %v", err)
		}
		link := filepath.Join(root, "runtime")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("create runtime symlink: %v", err)
		}
		token := derivedTokenFixture(t, testSandboxID)
		if err := StageTokenFile(link, uint32(os.Getuid()), uint32(os.Getgid()), &token); err == nil {
			t.Fatal("runtime symlink accepted")
		}
		if !allZero(token[:]) {
			t.Fatal("failed staging did not clear token")
		}
	})
	for _, operation := range []string{"stage", "consume"} {
		t.Run(operation+" credential symlink", func(t *testing.T) {
			directory, uid, gid := credentialDirectoryFixture(t)
			target := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(target, bytes.Repeat([]byte{1}, tokenBytes), 0o600); err != nil {
				t.Fatalf("write symlink target: %v", err)
			}
			path := filepath.Join(directory, CredentialFileName)
			if err := os.Symlink(target, path); err != nil {
				t.Fatalf("create credential symlink: %v", err)
			}
			if operation == "stage" {
				token := derivedTokenFixture(t, testSandboxID)
				if err := StageTokenFile(directory, uid, gid, &token); err == nil {
					t.Fatal("credential symlink accepted for staging")
				}
			} else if _, err := ConsumeTokenFile(directory, uid, gid); err == nil {
				t.Fatal("credential symlink accepted for consumption")
			}
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("credential symlink changed: %v %v", info, err)
			}
		})
	}
	t.Run("wide target mode", func(t *testing.T) {
		directory, uid, gid := credentialDirectoryFixture(t)
		path := filepath.Join(directory, CredentialFileName)
		if err := os.WriteFile(path, bytes.Repeat([]byte{1}, tokenBytes), 0o644); err != nil {
			t.Fatalf("write unsafe credential: %v", err)
		}
		token := derivedTokenFixture(t, testSandboxID)
		if err := StageTokenFile(directory, uid, gid, &token); err == nil {
			t.Fatal("wide existing credential accepted")
		}
	})
	t.Run("wrong owner", func(t *testing.T) {
		directory, uid, gid := credentialDirectoryFixture(t)
		token := derivedTokenFixture(t, testSandboxID)
		if err := StageTokenFile(directory, uid+1, gid+1, &token); err == nil {
			t.Fatal("wrong directory owner accepted")
		}
	})
}
