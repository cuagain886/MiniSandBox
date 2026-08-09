package egressnft

import (
	"encoding/json"
	"strings"
	"testing"

	"minisandbox/internal/egresspolicy"
)

// TestBootstrapPayloadRoundTrip 验证可重连控制协议能够嵌入不带二次长度前缀的
// bootstrap JSON，同时继续复用封闭 schema 与策略校验。
func TestBootstrapPayloadRoundTrip(t *testing.T) {
	policy := testPolicy(t)
	want := egressnftBootstrap(policy)
	payload, err := MarshalBootstrap(want)
	if err != nil {
		t.Fatalf("marshal bootstrap payload: %v", err)
	}
	got, err := ParseBootstrap(payload)
	if err != nil {
		t.Fatalf("parse bootstrap payload: %v", err)
	}
	if got.Policy.Hash != want.Policy.Hash || got.NetworkNamespace != want.NetworkNamespace ||
		got.ImageDigest != want.ImageDigest || got.AnchorUID != want.AnchorUID || got.AnchorGID != want.AnchorGID {
		t.Fatalf("bootstrap payload mismatch: got=%+v want=%+v", got, want)
	}
}

func egressnftBootstrap(policy egresspolicy.Policy) Bootstrap {
	return Bootstrap{
		Policy: policy, NetworkNamespace: "linux-netns:4:4026533000",
		ImageDigest: "registry.example/egressd@sha256:" + strings.Repeat("a", 64),
		AnchorUID:   65532, AnchorGID: 65532,
	}
}

// TestParseBootstrapRejections 锁定大小、JSON schema、版本和策略身份的 fail-closed
// 行为；控制流 framing 的拒绝测试位于 egresscontrol。
func TestParseBootstrapRejections(t *testing.T) {
	policy := testPolicy(t)
	validPayload := policyPayload(t, policy)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty input", data: nil},
		{name: "oversized payload", data: []byte(strings.Repeat("x", MaxBootstrapBytes+1))},
		{name: "trailing JSON value", data: append(validPayload, []byte(" {}")...)},
		{name: "unknown field", data: []byte(strings.TrimSuffix(string(validPayload), "}") + `,"extra":true}`)},
		{name: "duplicate field", data: []byte(strings.TrimSuffix(string(validPayload), "}") + `,"protocol_version":1}`)},
		{name: "malformed JSON", data: []byte("{")},
		{name: "missing deny sets", data: []byte(`{"protocol_version":1,"rule_schema_version":1,"policy_hash":"` + policy.Hash + `"}`)},
	}

	mutations := []struct {
		name   string
		mutate func(*bootstrapWire)
	}{
		{name: "protocol mismatch", mutate: func(w *bootstrapWire) { w.ProtocolVersion++ }},
		{name: "schema mismatch", mutate: func(w *bootstrapWire) { w.RuleSchemaVersion++ }},
		{name: "hash mismatch", mutate: func(w *bootstrapWire) { w.PolicyHash = strings.Repeat("0", 64) }},
		{name: "IPv4 in IPv6 set", mutate: func(w *bootstrapWire) { w.IPv6Denied = []string{"10.0.0.0/8"} }},
		{name: "IPv6 in IPv4 set", mutate: func(w *bootstrapWire) { w.IPv4Denied = []string{"fc00::/7"} }},
		{name: "noncanonical prefix", mutate: func(w *bootstrapWire) { w.IPv4Denied = append([]string{"8.8.8.1/24"}, w.IPv4Denied...) }},
		{name: "duplicate prefix", mutate: func(w *bootstrapWire) { w.IPv4Denied = append(w.IPv4Denied, w.IPv4Denied[0]) }},
	}
	for _, mutation := range mutations {
		wire := wirePolicy(policy)
		mutation.mutate(&wire)
		payload, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal mutation: %v", err)
		}
		tests = append(tests, struct {
			name string
			data []byte
		}{name: mutation.name, data: payload})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseBootstrap(test.data); err == nil {
				t.Fatal("expected bootstrap rejection")
			}
		})
	}
}

func testPolicy(t *testing.T) egresspolicy.Policy {
	t.Helper()
	policy, err := egresspolicy.Build([]string{"8.8.8.0/24", "2001:4860::/32"}, nil)
	if err != nil {
		t.Fatalf("build test policy: %v", err)
	}
	return policy
}

func wirePolicy(policy egresspolicy.Policy) bootstrapWire {
	wire := bootstrapWire{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, IPv4Denied: make([]string, len(policy.IPv4)), IPv6Denied: make([]string, len(policy.IPv6)),
		NetworkNamespace: "linux-netns:4:4026533000",
		ImageDigest:      "registry.example/egressd@sha256:" + strings.Repeat("a", 64),
		AnchorUID:        65532,
		AnchorGID:        65532,
	}
	for index, prefix := range policy.IPv4 {
		wire.IPv4Denied[index] = prefix.String()
	}
	for index, prefix := range policy.IPv6 {
		wire.IPv6Denied[index] = prefix.String()
	}
	return wire
}

func policyPayload(t *testing.T, policy egresspolicy.Policy) []byte {
	t.Helper()
	payload, err := json.Marshal(wirePolicy(policy))
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	return payload
}
