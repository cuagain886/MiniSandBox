package contract_test

import (
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionEventFixtures 固定八种事件，并证明非零退出码仍使用 exited。
func TestExecutionEventFixtures(t *testing.T) {
	for _, name := range []string{
		"started.json", "stdout.json", "stderr.json", "output-limit-reached.json",
		"exited-nonzero.json", "failed.json", "cancelled.json", "timed-out.json",
	} {
		event := decodeExecutionFixture[protocol.ExecutionEvent](t, name)
		if err := event.Validate(); err != nil {
			t.Errorf("%s: invalid event: %v", name, err)
		}
	}
	exited := decodeExecutionFixture[protocol.ExecutionEvent](t, "exited-nonzero.json")
	if exited.Type != protocol.EventExited || exited.ExitCode == nil || *exited.ExitCode != 7 {
		t.Fatalf("nonzero exit must remain exited: %#v", exited)
	}
}
