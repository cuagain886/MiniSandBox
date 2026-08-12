package metrics

import "testing"

// TestExecutionCountersNormalizeCardinality 验证 execution 指标固定名称和非法枚举归一。
func TestExecutionCountersNormalizeCardinality(t *testing.T) {
	registry := NewRegistry()
	counters, err := NewExecutionCounters(registry)
	if err != nil {
		t.Fatal(err)
	}
	counters.ObserveExecutionRequest("foreground", "accepted")
	counters.ObserveExecutionRequest("sandbox-secret", "raw-error-secret")
	counters.ObserveForegroundTerminal("timed_out")
	families, err := registry.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 2 {
		t.Fatalf("families: %d", len(families))
	}
	for _, family := range families {
		assertSafeMetricLabels(t, family)
	}
}
