package application

import (
	"errors"
	"strings"
	"testing"

	"minisandbox/internal/domain"
)

// TestHashCanonicalCreateRequestGolden 固定 domain separator 与 SHA-256 编码。
func TestHashCanonicalCreateRequestGolden(t *testing.T) {
	canonical := []byte(`{"contract_version":"lifecycle.create.v1","image":"docker.io/library/alpine","ttl_seconds":{"presence":"absent"},"network":{"outbound":false}}`)
	got, err := HashCanonicalCreateRequest(canonical)
	if err != nil {
		t.Fatalf("hash canonical request: %v", err)
	}
	const want = "7e2619689b188af5d941cf6e668028231cc763b798448642080d496257ba7144"
	if got != want {
		t.Fatalf("request hash: got %s, want %s", got, want)
	}
}

// TestHashCanonicalCreateRequestStableAndSensitive 验证相同 bytes 稳定且单字段差异改变 hash。
func TestHashCanonicalCreateRequestStableAndSensitive(t *testing.T) {
	first, err := HashCanonicalCreateRequest([]byte(`{"network":{"outbound":false}}`))
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	repeated, _ := HashCanonicalCreateRequest([]byte(`{"network":{"outbound":false}}`))
	different, _ := HashCanonicalCreateRequest([]byte(`{"network":{"outbound":true}}`))
	if first != repeated || first == different {
		t.Fatalf("unexpected hash stability: first=%s repeated=%s different=%s", first, repeated, different)
	}
	if len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("hash is not lowercase SHA-256 hex: %q", first)
	}
}

// TestHashCanonicalCreateRequestRejectsInvalidSize 验证空值和超大输入不参与 hash。
func TestHashCanonicalCreateRequestRejectsInvalidSize(t *testing.T) {
	for _, input := range [][]byte{nil, {}, make([]byte, maxCanonicalCreateBytes+1)} {
		got, err := HashCanonicalCreateRequest(input)
		if !errors.Is(err, domain.ErrInvalid) || got != "" {
			t.Fatalf("size %d: got %q/%v, want empty ErrInvalid", len(input), got, err)
		}
	}
	maximum := make([]byte, maxCanonicalCreateBytes)
	if got, err := HashCanonicalCreateRequest(maximum); err != nil || len(got) != 64 {
		t.Fatalf("maximum canonical input: %q/%v", got, err)
	}
}

// TestHashCanonicalCreateRequestDistinguishesCreateFields 验证 image、TTL 和 outbound 差异均改变身份。
func TestHashCanonicalCreateRequestDistinguishesCreateFields(t *testing.T) {
	ttl60, ttl61 := int64(60), int64(61)
	requests := []CanonicalCreateRequest{
		{Image: "alpine", TTLSeconds: &ttl60},
		{Image: "busybox", TTLSeconds: &ttl60},
		{Image: "alpine", TTLSeconds: &ttl61},
		{Image: "alpine", TTLSeconds: &ttl60, Outbound: true},
	}
	hashes := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		canonical, err := CanonicalizeCreateRequest(request)
		if err != nil {
			t.Fatalf("canonicalize %#v: %v", request, err)
		}
		hash, err := HashCanonicalCreateRequest(canonical)
		if err != nil {
			t.Fatalf("hash %#v: %v", request, err)
		}
		hashes[hash] = struct{}{}
	}
	if len(hashes) != len(requests) {
		t.Fatalf("field differences collapsed into %d hashes", len(hashes))
	}
}
