package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type snapshotSourceFake struct {
	records   []domain.Sandbox
	anomalies []storeport.RuntimeAnomaly
	err       error
	calls     int
}

func (s *snapshotSourceFake) ListAll(context.Context) ([]domain.Sandbox, error) {
	s.calls++
	return s.records, s.err
}
func (s *snapshotSourceFake) ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error) {
	s.calls++
	return s.anomalies, s.err
}

// TestSnapshotGaugesRetainLastSuccessAndScrapeDoesNotQueryStore 验证失败采样保留旧值且 gather 无 Store I/O。
func TestSnapshotGaugesRetainLastSuccessAndScrapeDoesNotQueryStore(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	registry := NewRegistry()
	gauges, err := NewSnapshotGauges(registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	source := &snapshotSourceFake{records: []domain.Sandbox{{ObservedState: domain.StateRunning}, {ObservedState: domain.StateFailed, Reason: domain.SandboxReasonCleanupPending}}, anomalies: make([]storeport.RuntimeAnomaly, 2)}
	if err := gauges.SampleStore(context.Background(), source, time.Second, 10); err != nil {
		t.Fatal(err)
	}
	calls := source.calls
	gauges.UpdateScheduler(3, 2)
	for range 3 {
		if _, err := registry.Gatherer().Gather(); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls != calls {
		t.Fatalf("scrape queried Store: before=%d after=%d", calls, source.calls)
	}
	source.err = errors.New("database unavailable")
	if err := gauges.SampleStore(context.Background(), source, time.Second, 10); err == nil {
		t.Fatal("expected sample failure")
	}
	families, _ := registry.Gatherer().Gather()
	if len(families) < 5 {
		t.Fatalf("last snapshot lost: %d families", len(families))
	}
}

// TestSnapshotGaugesRejectRowOverflowBeforePublish 验证行数边界不会用伪造零值覆盖旧 snapshot。
func TestSnapshotGaugesRejectRowOverflowBeforePublish(t *testing.T) {
	registry := NewRegistry()
	gauges, _ := NewSnapshotGauges(registry, time.Now)
	source := &snapshotSourceFake{records: make([]domain.Sandbox, 2)}
	if err := gauges.SampleStore(context.Background(), source, time.Second, 1); err == nil {
		t.Fatal("expected row limit error")
	}
}
