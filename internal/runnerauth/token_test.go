package runnerauth

import (
	"bytes"
	"encoding/hex"
	"testing"

	"minisandbox/internal/config"
	"minisandbox/internal/runnerbootstrap"
)

const testSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

// TestDeriveTokenStableAndSeparated 钉死 HMAC domain separation 的稳定结果，
// 并证明不同 sandbox ID 不会共享 token。
func TestDeriveTokenStableAndSeparated(t *testing.T) {
	var key MasterKey
	for index := range key {
		key[index] = byte(index)
	}
	first, err := DeriveToken(&key, testSandboxID)
	if err != nil {
		t.Fatalf("derive first token: %v", err)
	}
	defer first.Clear()
	want := "58efb78991e2b38c79118f8d6511bd8dcf756f6d6b8567504ee06ffb37e465db"
	if got := hex.EncodeToString(first[:]); got != want {
		t.Fatalf("derived token drift: got %s, want %s", got, want)
	}
	second, err := DeriveToken(&key, "10010203-0405-4607-8809-0a0b0c0d0e0f")
	if err != nil {
		t.Fatalf("derive second token: %v", err)
	}
	defer second.Clear()
	if first == second {
		t.Fatal("different sandbox IDs derived the same token")
	}
	again, err := DeriveToken(&key, testSandboxID)
	if err != nil {
		t.Fatalf("derive stable token: %v", err)
	}
	defer again.Clear()
	if first != again {
		t.Fatal("same key and sandbox ID did not derive a stable token")
	}
}

// TestBootstrapJSONDoesNotCarryToken 验证 bootstrap JSON 仅描述非秘密配置；派生 token
// 及其一次性文件名均不会被序列化到该协议中。
func TestBootstrapJSONDoesNotCarryToken(t *testing.T) {
	var key MasterKey
	for index := range key {
		key[index] = byte(index + 1)
	}
	token, err := DeriveToken(&key, testSandboxID)
	if err != nil {
		t.Fatalf("derive token: %v", err)
	}
	defer token.Clear()
	bootstrap, err := runnerbootstrap.FromConfig(
		config.Default(),
		testSandboxID,
		0,
		0,
	)
	if err != nil {
		t.Fatalf("build bootstrap config: %v", err)
	}
	encoded, err := runnerbootstrap.Marshal(bootstrap)
	if err != nil {
		t.Fatalf("marshal bootstrap config: %v", err)
	}
	if bytes.Contains(encoded, token[:]) ||
		bytes.Contains(encoded, []byte(hex.EncodeToString(token[:]))) ||
		bytes.Contains(encoded, []byte(CredentialFileName)) {
		t.Fatal("runner credential leaked into bootstrap JSON")
	}
}

// TestDeriveTokenRejectsInvalidInputs 验证 nil/全零 key 与非规范 sandbox ID
// 均不会产生 token。
func TestDeriveTokenRejectsInvalidInputs(t *testing.T) {
	var zero MasterKey
	if _, err := DeriveToken(nil, testSandboxID); err == nil {
		t.Fatal("nil master key accepted")
	}
	if _, err := DeriveToken(&zero, testSandboxID); err == nil {
		t.Fatal("all-zero master key accepted")
	}
	zero[0] = 1
	for _, id := range []string{"", "../sandbox", "00010203-0405-1607-8809-0a0b0c0d0e0f"} {
		if _, err := DeriveToken(&zero, id); err == nil {
			t.Fatalf("invalid sandbox ID accepted: %q", id)
		}
	}
}
