package protocol

import (
	"encoding/json"
	"testing"
)

// TestReadinessResponseJSON 验证 readiness wire 字段和稳定状态值。
func TestReadinessResponseJSON(t *testing.T) {
	response := ReadinessResponse{
		Status: ReadinessStatusNotReady,
		Components: []ReadinessComponent{
			{
				Name:   ReadinessComponentStore,
				Status: ReadinessStatusReady,
			},
			{
				Name:   ReadinessComponentDocker,
				Status: ReadinessStatusNotReady,
			},
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal readiness response: %v", err)
	}
	const expected = `{"status":"not_ready","components":[{"name":"store","status":"ready"},{"name":"docker","status":"not_ready"}]}`
	if string(encoded) != expected {
		t.Fatalf("JSON: got %s, want %s", encoded, expected)
	}
}
