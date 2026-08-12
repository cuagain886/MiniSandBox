package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type diagnosticsFake struct {
	records                          []domain.Sandbox
	anomalies                        []storeport.RuntimeAnomaly
	storeErr, anomalyErr, runtimeErr error
	scheduler                        SchedulerDiagnostics
	revision                         uint64
}

func (f *diagnosticsFake) ListAll(context.Context) ([]domain.Sandbox, error) {
	return f.records, f.storeErr
}
func (f *diagnosticsFake) ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error) {
	return f.anomalies, f.anomalyErr
}
func (f *diagnosticsFake) Diagnostics(context.Context) (RuntimeDiagnostics, error) {
	return RuntimeDiagnostics{ManagedSandboxes: 2, OutboundSandboxes: 1}, f.runtimeErr
}
func (f *diagnosticsFake) RunnerDiagnostics(context.Context) (RunnerDiagnostics, error) {
	return RunnerDiagnostics{Ready: 1}, nil
}
func (f *diagnosticsFake) DiagnosticsScheduler() SchedulerDiagnostics { return f.scheduler }

type diagnosticsSchedulerFake struct{ value SchedulerDiagnostics }

func (f diagnosticsSchedulerFake) Diagnostics() SchedulerDiagnostics { return f.value }

// TestDiagnosticsSnapshotSupportsCompleteAndPartialSections 验证完整与分段失败均不泄露 cause。
func TestDiagnosticsSnapshotSupportsCompleteAndPartialSections(t *testing.T) {
	fake := &diagnosticsFake{records: []domain.Sandbox{{ObservedState: domain.StateRunning}}, anomalies: []storeport.RuntimeAnomaly{{RuntimeAnomalyObservation: storeport.RuntimeAnomalyObservation{Classification: storeport.RuntimeAnomalyUnknownSchema}}}}
	service, _ := NewDiagnosticsService(fake, fake, diagnosticsSchedulerFake{SchedulerDiagnostics{QueueDepth: 2, ActiveWorkers: 1}}, time.Second, time.Now)
	snapshot := service.Snapshot(context.Background())
	if snapshot.Store.Status != "available" || snapshot.Anomalies.Classifications["unknown_schema"] != 1 || snapshot.Runtime.Counts["managed_sandboxes"] != 2 {
		t.Fatalf("snapshot: %#v", snapshot)
	}
	fake.runtimeErr = errors.New("secret docker host /var/run/docker.sock")
	partial := service.Snapshot(context.Background())
	if partial.Runtime.Status != "unavailable" || partial.Store.Status != "available" {
		t.Fatalf("partial: %#v", partial)
	}
	encoded, _ := json.Marshal(partial)
	if strings.Contains(string(encoded), "docker.sock") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("secret leaked: %s", encoded)
	}
}

// TestDiagnosticsSnapshotDoesNotMutateObservedState 验证调用前后测试端口状态不变。
func TestDiagnosticsSnapshotDoesNotMutateObservedState(t *testing.T) {
	fake := &diagnosticsFake{revision: 7}
	service, _ := NewDiagnosticsService(fake, fake, diagnosticsSchedulerFake{}, time.Second, time.Now)
	_ = service.Snapshot(context.Background())
	if fake.revision != 7 {
		t.Fatalf("revision changed: %d", fake.revision)
	}
}

type diagnosticsRunnerFake struct{ err error }

func (f diagnosticsRunnerFake) Diagnostics(context.Context) (RunnerDiagnostics, error) {
	return RunnerDiagnostics{Ready: 2, Unavailable: 1}, f.err
}

// TestDiagnosticsSnapshotIncludesIndependentRunnerSection 验证 runner 聚合故障不会污染其他只读 section。
func TestDiagnosticsSnapshotIncludesIndependentRunnerSection(t *testing.T) {
	fake := &diagnosticsFake{}
	runner := diagnosticsRunnerFake{}
	service, _ := NewDiagnosticsService(fake, fake, diagnosticsSchedulerFake{}, time.Second, time.Now, runner)
	snapshot := service.Snapshot(context.Background())
	if snapshot.Runner.Status != "available" || snapshot.Runner.Counts["ready"] != 2 || snapshot.Runner.Counts["unavailable"] != 1 {
		t.Fatalf("runner section: %#v", snapshot.Runner)
	}
	runner.err = errors.New("runner socket path secret")
	service, _ = NewDiagnosticsService(fake, fake, diagnosticsSchedulerFake{}, time.Second, time.Now, runner)
	snapshot = service.Snapshot(context.Background())
	if snapshot.Runner.Status != "unavailable" || snapshot.Store.Status != "available" {
		t.Fatalf("partial runner failure: %#v", snapshot)
	}
}
