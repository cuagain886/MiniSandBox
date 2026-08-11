package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"minisandbox/internal/domain"
)

// TestCanonicalizeCreateRequestGolden 固定字段顺序、版本和 absent sentinel。
func TestCanonicalizeCreateRequestGolden(t *testing.T) {
	got, err := CanonicalizeCreateRequest(CanonicalCreateRequest{Image: "alpine", Outbound: false})
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := `{"contract_version":"lifecycle.create.v1","image":"docker.io/library/alpine","ttl_seconds":{"presence":"absent"},"network":{"outbound":false}}`
	if string(got) != want {
		t.Fatalf("canonical bytes:\n got: %s\nwant: %s", got, want)
	}
}

// TestCanonicalizeCreateRequestEquivalentSemantics 验证等价 image、boolean 和 TTL 逐字节相同。
func TestCanonicalizeCreateRequestEquivalentSemantics(t *testing.T) {
	ttlA, ttlB := int64(3600), int64(3600)
	requests := []CanonicalCreateRequest{
		{Image: "alpine", TTLSeconds: &ttlA, Outbound: true},
		{Image: "docker.io/library/alpine", TTLSeconds: &ttlB, Outbound: true},
	}
	first, err := CanonicalizeCreateRequest(requests[0])
	if err != nil {
		t.Fatalf("first canonicalization: %v", err)
	}
	second, err := CanonicalizeCreateRequest(requests[1])
	if err != nil {
		t.Fatalf("second canonicalization: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("equivalent requests differ:\n%s\n%s", first, second)
	}
}

// TestCanonicalizeCreateRequestPreservesTTLPresence 验证缺失 TTL 不等价于当前默认值。
func TestCanonicalizeCreateRequestPreservesTTLPresence(t *testing.T) {
	defaultTTL := int64(1800)
	absent, err := CanonicalizeCreateRequest(CanonicalCreateRequest{Image: "alpine"})
	if err != nil {
		t.Fatalf("absent TTL: %v", err)
	}
	present, err := CanonicalizeCreateRequest(CanonicalCreateRequest{Image: "alpine", TTLSeconds: &defaultTTL})
	if err != nil {
		t.Fatalf("present TTL: %v", err)
	}
	if bytes.Equal(absent, present) {
		t.Fatal("absent TTL collapsed into the current server default")
	}
}

// TestCanonicalizeCreateRequestIgnoresMapIteration 验证 JSON map 输入顺序不会影响 typed model。
func TestCanonicalizeCreateRequestIgnoresMapIteration(t *testing.T) {
	left := []byte(`{"image":"alpine","network":{"outbound":true},"ttl_seconds":60}`)
	right := []byte(`{"ttl_seconds":60,"network":{"outbound":true},"image":"alpine"}`)
	decode := func(content []byte) CanonicalCreateRequest {
		var wire struct {
			Image      string `json:"image"`
			TTLSeconds *int64 `json:"ttl_seconds"`
			Network    struct {
				Outbound bool `json:"outbound"`
			} `json:"network"`
		}
		if err := json.Unmarshal(content, &wire); err != nil {
			t.Fatalf("decode test request: %v", err)
		}
		return CanonicalCreateRequest{Image: wire.Image, TTLSeconds: wire.TTLSeconds, Outbound: wire.Network.Outbound}
	}
	a, _ := CanonicalizeCreateRequest(decode(left))
	b, _ := CanonicalizeCreateRequest(decode(right))
	if !bytes.Equal(a, b) {
		t.Fatalf("map iteration changed canonical bytes:\n%s\n%s", a, b)
	}
}

// TestCanonicalizeCreateRequestRejectsInvalidImage 验证错误不回显原始 image。
func TestCanonicalizeCreateRequestRejectsInvalidImage(t *testing.T) {
	secret := "registry.invalid/user:password@not-a-reference"
	_, err := CanonicalizeCreateRequest(CanonicalCreateRequest{Image: secret})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte(secret)) {
		t.Fatal("canonicalization error leaked raw image")
	}
}
