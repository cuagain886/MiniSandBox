package metrics

import (
	"sync"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// TestOperationCountersExposeContractBranches 验证六组 counter 的名称、分支与未知值归一行为。
func TestOperationCountersExposeContractBranches(t *testing.T) {
	registry := NewRegistry()
	counters, err := NewOperationCounters(registry)
	if err != nil {
		t.Fatal(err)
	}
	counters.ObserveCreate("accepted")
	counters.ObserveCreate("secret-image-value")
	counters.ObserveReconcile("recover", "retry_scheduled")
	counters.ObserveRetryScheduled("delete", "cleanup_pending")
	counters.ObserveLeaseExpired()
	counters.ObserveOrphan("unknown_schema")
	counters.ObserveDocker("ensure_sandbox", "retryable_error")
	families, err := registry.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"minisandbox_sandbox_create_requests_total": false, "minisandbox_reconcile_total": false,
		"minisandbox_retry_scheduled_total": false, "minisandbox_lease_expired_total": false,
		"minisandbox_orphan_observations_total": false, "minisandbox_runtime_docker_operations_total": false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
		assertSafeMetricLabels(t, family)
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing metric %s", name)
		}
	}
}

// TestOperationCountersSupportConcurrentUpdates 验证业务封装在并发更新和 gather 时保持安全。
func TestOperationCountersSupportConcurrentUpdates(t *testing.T) {
	registry := NewRegistry()
	counters, err := NewOperationCounters(registry)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				counters.ObserveReconcile("create", "converged")
				counters.ObserveDocker("inventory", "success")
				_, _ = registry.Gatherer().Gather()
			}
		}()
	}
	wait.Wait()
}

func assertSafeMetricLabels(t *testing.T, family *dto.MetricFamily) {
	t.Helper()
	allowedNames := map[string]struct{}{"result": {}, "operation": {}, "error_code": {}, "classification": {}, "mode": {}}
	for _, metric := range family.Metric {
		for _, label := range metric.Label {
			if _, ok := allowedNames[label.GetName()]; !ok {
				t.Errorf("unsafe label name %q", label.GetName())
			}
			for _, forbidden := range []string{"sandbox", "image", "key", "message", "secret"} {
				if label.GetValue() == forbidden || label.GetValue() == "secret-image-value" {
					t.Errorf("unsafe label value %q", label.GetValue())
				}
			}
		}
	}
}
