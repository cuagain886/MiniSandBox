package egresscontrol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

const (
	testRequestID = "00112233445566778899aabbccddeeff"
	testNonce     = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

// TestRequestStreamRoundTrip 验证 bootstrap 与 inspect 可以在同一持续输入流中逐帧
// 解码，且 inspect 不携带任何可修改策略的 payload。
func TestRequestStreamRoundTrip(t *testing.T) {
	bootstrap := testBootstrap(t)
	bootstrapFrame, err := EncodeRequest(Request{
		Type: RequestBootstrap, RequestID: testRequestID, Nonce: testNonce, Bootstrap: &bootstrap,
	})
	if err != nil {
		t.Fatalf("encode bootstrap request: %v", err)
	}
	inspectFrame, err := EncodeRequest(Request{Type: RequestInspect, RequestID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("encode inspect request: %v", err)
	}
	stream := bytes.NewReader(append(bootstrapFrame, inspectFrame...))
	first, err := ReadRequest(stream)
	if err != nil || first.Type != RequestBootstrap || first.Bootstrap == nil || first.Bootstrap.Policy.Hash != bootstrap.Policy.Hash {
		t.Fatalf("read bootstrap request: request=%+v err=%v", first, err)
	}
	second, err := ReadRequest(stream)
	if err != nil || second.Type != RequestInspect || second.Bootstrap != nil {
		t.Fatalf("read inspect request: request=%+v err=%v", second, err)
	}
}

// TestResponseRoundTrip 验证 attestation 与当前请求的关联标识被完整保留。
func TestResponseRoundTrip(t *testing.T) {
	attestation := testAttestation(t)
	encoded, err := EncodeResponse(Response{RequestID: testRequestID, Nonce: testNonce, Attestation: attestation})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	decoded, err := ReadResponse(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if decoded.RequestID != testRequestID || decoded.Nonce != testNonce || decoded.Attestation.PolicyHash != attestation.PolicyHash {
		t.Fatalf("response mismatch: %+v", decoded)
	}
}

// TestControlProtocolRejectsAmbiguousFrames 锁定超限、未知/重复字段、错误类型、
// 可预测关联字段和 inspect 策略注入均 fail closed。
func TestControlProtocolRejectsAmbiguousFrames(t *testing.T) {
	bootstrapPayload, err := egressnft.MarshalBootstrap(testBootstrap(t))
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	valid := requestWire{
		ProtocolVersion: egresspolicy.CurrentProtocolVersion, Type: RequestBootstrap,
		RequestID: testRequestID, Nonce: testNonce, Bootstrap: bootstrapPayload,
	}
	validPayload, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid request: %v", err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "zero length", data: make([]byte, 4)},
		{name: "oversized", data: lengthOnly(MaxRequestBytes + 1)},
		{name: "truncated", data: append(lengthOnly(10), []byte("{}")...)},
		{name: "unknown field", data: frame([]byte(strings.TrimSuffix(string(validPayload), "}") + `,"extra":true}`))},
		{name: "duplicate field", data: frame([]byte(strings.TrimSuffix(string(validPayload), "}") + `,"nonce":"` + testNonce + `"}`))},
		{name: "uppercase nonce", data: frame(mutateRequest(t, valid, func(w *requestWire) { w.Nonce = strings.ToUpper(w.Nonce) }))},
		{name: "unknown type", data: frame(mutateRequest(t, valid, func(w *requestWire) { w.Type = "update" }))},
		{name: "inspect with bootstrap", data: frame(mutateRequest(t, valid, func(w *requestWire) { w.Type = RequestInspect }))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadRequest(bytes.NewReader(test.data)); err == nil {
				t.Fatal("ambiguous control request accepted")
			}
		})
	}
}

// TestNewCorrelation 验证每次关联数据具有固定强度、合法编码且不会直接复用。
func TestNewCorrelation(t *testing.T) {
	firstID, firstNonce, err := NewCorrelation()
	if err != nil {
		t.Fatalf("generate first correlation: %v", err)
	}
	secondID, secondNonce, err := NewCorrelation()
	if err != nil {
		t.Fatalf("generate second correlation: %v", err)
	}
	if !validCorrelation(firstID, firstNonce) || !validCorrelation(secondID, secondNonce) ||
		firstID == secondID || firstNonce == secondNonce {
		t.Fatalf("invalid or reused correlation: first=%q/%q second=%q/%q", firstID, firstNonce, secondID, secondNonce)
	}
}

func testBootstrap(t *testing.T) egressnft.Bootstrap {
	t.Helper()
	policy, err := egresspolicy.Build(nil, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return egressnft.Bootstrap{
		Policy: policy, NetworkNamespace: "linux-netns:4:4026533000",
		ImageDigest: "registry.example/egressd@sha256:" + strings.Repeat("a", 64),
		AnchorUID:   65532, AnchorGID: 65532,
	}
}

func testAttestation(t *testing.T) egressanchor.Attestation {
	t.Helper()
	bootstrap := testBootstrap(t)
	return egressanchor.Attestation{
		ProtocolVersion: bootstrap.Policy.ProtocolVersion, RuleSchemaVersion: bootstrap.Policy.RuleSchemaVersion,
		PolicyHash: bootstrap.Policy.Hash, NetworkNamespace: bootstrap.NetworkNamespace,
		ImageDigest: bootstrap.ImageDigest, CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
}

func mutateRequest(t *testing.T, wire requestWire, mutate func(*requestWire)) []byte {
	t.Helper()
	mutate(&wire)
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal request mutation: %v", err)
	}
	return payload
}

func frame(payload []byte) []byte {
	result := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(result, uint32(len(payload)))
	copy(result[4:], payload)
	return result
}

func lengthOnly(length int) []byte {
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, uint32(length))
	return result
}
