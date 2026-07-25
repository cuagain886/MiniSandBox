package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestErrorResponseJSONRoundTrip 验证公共错误 envelope 的字段名和必填布尔值。
func TestErrorResponseJSONRoundTrip(t *testing.T) {
	original := ErrorResponse{
		Error: ErrorDetail{
			Code:      "INVALID_REQUEST",
			Message:   "Request is invalid.",
			RequestID: "req-01",
			Retryable: false,
		},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	const expected = `{"error":{"code":"INVALID_REQUEST","message":"Request is invalid.","request_id":"req-01","retryable":false}}`
	if got := string(encoded); got != expected {
		t.Fatalf("unexpected JSON: got %s, want %s", got, expected)
	}

	var decoded ErrorResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, original)
	}
}
