package application

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"minisandbox/internal/domain"
)

// TestNewLocalIdempotencyKeyBoundaries 验证允许字符和 1..128 字节边界。
func TestNewLocalIdempotencyKeyBoundaries(t *testing.T) {
	for _, value := range []string{"a", "AZaz09._:-", strings.Repeat("k", 128)} {
		key, err := NewLocalIdempotencyKey(value)
		if err != nil || key.ScopeID() != "local:v1" || key.Value() != value {
			t.Fatalf("valid key %q: %#v/%v", value, key, err)
		}
	}
	for _, value := range []string{"", "space key", "comma,key", "ümlaut", strings.Repeat("k", 129)} {
		if key, err := NewLocalIdempotencyKey(value); !errors.Is(err, domain.ErrInvalid) || key != (IdempotencyKey{}) {
			t.Fatalf("invalid key length %d: %#v/%v", len(value), key, err)
		}
	}
}

// TestIdempotencyKeyScopeIsolation 验证相同 raw key 在不同 scope 下不是同一身份。
func TestIdempotencyKeyScopeIsolation(t *testing.T) {
	local, err := newIdempotencyKey("local:v1", "same-key")
	if err != nil {
		t.Fatalf("local key: %v", err)
	}
	future, err := newIdempotencyKey("tenant:v2", "same-key")
	if err != nil {
		t.Fatalf("future key: %v", err)
	}
	if local == future || local.ScopeID() == future.ScopeID() {
		t.Fatalf("scope identity collapsed: %#v %#v", local, future)
	}
}

// TestIdempotencyKeyLoggingRedactsRawValue 验证 fmt、slog 和错误均不含 raw key。
func TestIdempotencyKeyLoggingRedactsRawValue(t *testing.T) {
	const sentinel = "secret-idempotency-key"
	key, err := NewLocalIdempotencyKey(sentinel)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("create", "idempotency_key", key)
	combined := fmt.Sprintf("%s\n%+v\n%s", key, key, output.String())
	if strings.Contains(combined, sentinel) || strings.Contains(combined, key.ScopeID()) {
		t.Fatalf("formatted/logged key leaked identity: %s", combined)
	}
	if !strings.Contains(output.String(), `"idempotency_key":true`) {
		t.Fatalf("safe presence field missing: %s", output.String())
	}
}
