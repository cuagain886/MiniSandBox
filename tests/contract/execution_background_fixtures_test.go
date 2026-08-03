package contract_test

import (
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionBackgroundFixtures 固定 descriptor、status 和 logs page。
func TestExecutionBackgroundFixtures(t *testing.T) {
	descriptor := decodeExecutionFixture[protocol.ExecutionDescriptor](t, "descriptor.json")
	status := decodeExecutionFixture[protocol.ExecutionStatus](t, "status.json")
	logs := decodeExecutionFixture[protocol.ExecutionLogPage](t, "logs.json")
	if descriptor.ExecutionID != "e1" || status.TerminalEvent == nil ||
		!status.TerminalEvent.Terminal() || logs.NextCursor != 5 || !logs.Complete {
		t.Fatalf("invalid background fixtures: %#v %#v %#v", descriptor, status, logs)
	}
}
