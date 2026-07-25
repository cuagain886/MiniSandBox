package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestCreateSandboxRequestJSONRoundTrip 验证 Phase 1 创建请求只包含镜像字段。
func TestCreateSandboxRequestJSONRoundTrip(t *testing.T) {
	original := CreateSandboxRequest{Image: "alpine:3.22"}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	if got, want := string(encoded), `{"image":"alpine:3.22"}`; got != want {
		t.Fatalf("unexpected JSON: got %s, want %s", got, want)
	}

	var decoded CreateSandboxRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal create request: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, original)
	}
}

// TestCreateSandboxRequestHasOnlyImageField 防止后续实现提前暴露 Phase 1 不支持的字段。
func TestCreateSandboxRequestHasOnlyImageField(t *testing.T) {
	requestType := reflect.TypeOf(CreateSandboxRequest{})
	if got, want := requestType.NumField(), 1; got != want {
		t.Fatalf("unexpected field count: got %d, want %d", got, want)
	}
	if got, want := requestType.Field(0).Name, "Image"; got != want {
		t.Fatalf("unexpected request field: got %s, want %s", got, want)
	}
}
