package contract_test

import (
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionRequestFixture 固定前台请求字段和秒级 timeout。
func TestExecutionRequestFixture(t *testing.T) {
	request := decodeExecutionFixture[protocol.ExecuteRequest](t, "request-foreground.json")
	if request.TimeoutSeconds != 120 || request.Background || len(request.Argv) != 3 {
		t.Fatalf("unexpected foreground request: %#v", request)
	}
}
