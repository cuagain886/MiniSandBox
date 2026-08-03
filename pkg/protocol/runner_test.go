package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestExecuteRequestJSONRoundTrip 固定公共与内部 runner 共用的秒级请求字段。
func TestExecuteRequestJSONRoundTrip(t *testing.T) {
	original := ExecuteRequest{
		Argv:           []string{"go", "test", "./..."},
		Cwd:            "/workspace/project",
		Env:            map[string]string{"CI": "true"},
		TimeoutSeconds: 120,
		Background:     true,
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	const want = `{"argv":["go","test","./..."],"env":{"CI":"true"},"cwd":"/workspace/project","timeout_seconds":120,"background":true}`
	if string(encoded) != want {
		t.Fatalf("request JSON: got %s, want %s", encoded, want)
	}
	var decoded ExecuteRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, original)
	}
}
