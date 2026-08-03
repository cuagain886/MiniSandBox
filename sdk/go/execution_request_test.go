package sdk

import (
	"testing"
	"time"
)

// TestExecuteRequestWireMapping 验证 SDK duration 只以整秒映射到 wire。
func TestExecuteRequestWireMapping(t *testing.T) {
	request := ExecuteRequest{
		Argv:    []string{"go", "test", "./..."},
		Cwd:     "/workspace",
		Env:     map[string]string{"CI": "true"},
		Timeout: 90 * time.Second,
	}
	wire, err := request.wire(false)
	if err != nil {
		t.Fatalf("map request: %v", err)
	}
	if wire.TimeoutSeconds != 90 || wire.Background || wire.Cwd != "/workspace" {
		t.Fatalf("unexpected wire request: %#v", wire)
	}
	request.Timeout = time.Second + time.Millisecond
	if _, err := request.wire(false); err == nil {
		t.Fatal("fractional seconds must be rejected")
	}
}
