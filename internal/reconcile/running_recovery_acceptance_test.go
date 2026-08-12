package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	sqlitestore "minisandbox/internal/store/sqlite"
)

type acceptanceComputeRuntime struct {
	mu           sync.Mutex
	actual       runtimeport.ActualSandbox
	ensures      int
	replacements int
	deletes      int
	workspace    string
	lease        time.Time
}

func (r *acceptanceComputeRuntime) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensures++
	r.actual.State = runtimeport.ActualRunning
	return r.actual, nil
}
func (r *acceptanceComputeRuntime) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actual, nil
}
func (r *acceptanceComputeRuntime) Delete(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes++
	return nil
}
func (*acceptanceComputeRuntime) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}
func (r *acceptanceComputeRuntime) ReplaceCompute(_ context.Context, sandbox domain.Sandbox) (runtimeport.ActualSandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replacements++
	r.actual = runtimeport.ActualSandbox{ID: sandbox.ID, RuntimeID: "replacement", State: runtimeport.ActualRunning,
		SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1}
	return r.actual, nil
}

type acceptanceProbe struct {
	mu       sync.Mutex
	failures int
}

func (p *acceptanceProbe) Probe(context.Context, string, int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return errors.New("runner unavailable")
	}
	return nil
}

// TestRunningRecoveryWithSQLitePreservesWorkspaceAndLease 验证 stopped/missing compute
// 与三次 runner failure 的恢复只替换计算资源，workspace 哨兵和最新租约保持不变且资源唯一。
func TestRunningRecoveryWithSQLitePreservesWorkspaceAndLease(t *testing.T) {
	tests := []struct {
		name        string
		state       runtimeport.ActualState
		failures    int
		wantEnsure  int
		wantReplace int
	}{
		{name: "missing network none", state: runtimeport.ActualMissing, wantEnsure: 1},
		{name: "stopped network none", state: runtimeport.ActualStopped, wantEnsure: 1},
		{name: "runner threshold", state: runtimeport.ActualRunning, failures: 3, wantReplace: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sandboxd.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := database.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2028, 2, 3, 4, 5, 6, 0, time.UTC)
			record := cleanupRecoveryRecord(now)
			record.ID = "40010203-0405-4607-8809-0a0b0c0d0e0f"
			record.DesiredState, record.ObservedState = domain.DesiredRunning, domain.StateRunning
			if err := database.Create(ctx, record); err != nil {
				t.Fatal(err)
			}
			stored, _ := database.Get(ctx, record.ID)
			runtime := &acceptanceComputeRuntime{actual: runtimeport.ActualSandbox{ID: record.ID, RuntimeID: "original", State: test.state,
				SpecHash: record.SpecHash, RunnerProtocolVersion: 1}, workspace: "sentinel-content", lease: *record.ExpiresAt}
			probe := &acceptanceProbe{failures: test.failures}
			reconciler := New(database, runtime, probe)
			for attempts := 0; attempts < max(1, test.failures); attempts++ {
				if err := reconciler.Reconcile(ctx, record.ID); err != nil {
					t.Fatal(err)
				}
			}
			final, err := database.Get(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if final.ObservedState != domain.StateRunning || final.HealthFailureCount != 0 || runtime.ensures != test.wantEnsure ||
				runtime.replacements != test.wantReplace || runtime.deletes != 0 || runtime.workspace != "sentinel-content" || !runtime.lease.Equal(*stored.ExpiresAt) {
				t.Fatalf("recovery: final=%#v runtime=%#v", final, runtime)
			}
		})
	}
}

// TestRunningSpecDriftFailsClosedWithoutReplacement 验证已确认的 spec hash 漂移保持
// Failed/SPEC_DRIFT 供诊断，reconciler 不调用 Ensure、ReplaceCompute 或完整 Delete 覆盖证据。
func TestRunningSpecDriftFailsClosedWithoutReplacement(t *testing.T) {
	events := make([]string, 0)
	sandbox := runningRecoverySandbox(false)
	store := &runningRecoveryStore{newReconcileStore(&events, sandbox)}
	runtime := &runningRecoveryRuntime{events: &events, actual: runtimeport.ActualSandbox{
		ID: sandbox.ID, RuntimeID: "drifted", State: runtimeport.ActualRunning, SpecHash: "different", RunnerProtocolVersion: 1,
	}}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	if store.record.ObservedState != domain.StateFailed || store.record.Reason != domain.SandboxReasonSpecDrift {
		t.Fatalf("drift record: %#v", store.record)
	}
	assertEventAbsent(t, events, "runtime-ensure")
	assertEventAbsent(t, events, "runtime-replace-compute")
	assertEventAbsent(t, events, "runtime-delete")
}

var _ runtimeport.Runtime = (*acceptanceComputeRuntime)(nil)
var _ runtimeport.ComputeReplacer = (*acceptanceComputeRuntime)(nil)
