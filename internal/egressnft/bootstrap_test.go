package egressnft

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"minisandbox/internal/egresspolicy"
)

// TestReadBootstrap 验证唯一合法输入是完整、有界、版本匹配且 hash 正确的一帧。
func TestReadBootstrap(t *testing.T) {
	policy := testPolicy(t)
	got, err := ReadBootstrap(bytes.NewReader(framePolicy(t, policy)))
	if err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	if got.Policy.Hash != policy.Hash || len(got.Policy.IPv4) != len(policy.IPv4) || len(got.Policy.IPv6) != len(policy.IPv6) {
		t.Fatalf("decoded policy mismatch: %+v", got)
	}
}

// TestReadBootstrapRejections 锁定 framing、大小、EOF、JSON schema、版本和策略身份
// 的 fail-closed 行为。
func TestReadBootstrapRejections(t *testing.T) {
	policy := testPolicy(t)
	validPayload := policyPayload(t, policy)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty input", data: nil},
		{name: "empty frame", data: lengthOnly(0)},
		{name: "oversized frame", data: lengthOnly(MaxBootstrapBytes + 1)},
		{name: "early EOF", data: append(lengthOnly(10), []byte("{}")...)},
		{name: "trailing byte replay", data: append(frame(validPayload), 0)},
		{name: "trailing JSON value", data: frame(append(validPayload, []byte(" {}")...))},
		{name: "unknown field", data: frame([]byte(strings.TrimSuffix(string(validPayload), "}") + `,"extra":true}`))},
		{name: "duplicate field", data: frame([]byte(strings.TrimSuffix(string(validPayload), "}") + `,"protocol_version":1}`))},
		{name: "malformed JSON", data: frame([]byte("{"))},
		{name: "missing deny sets", data: frame([]byte(`{"protocol_version":1,"rule_schema_version":1,"policy_hash":"` + policy.Hash + `"}`))},
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
		}{name: mutation.name, data: frame(payload)})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadBootstrap(bytes.NewReader(test.data)); err == nil {
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

func framePolicy(t *testing.T, policy egresspolicy.Policy) []byte {
	return frame(policyPayload(t, policy))
}

func frame(payload []byte) []byte {
	result := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(result, uint32(len(payload)))
	copy(result[4:], payload)
	return result
}

func lengthOnly(length uint32) []byte {
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, length)
	return result
}
