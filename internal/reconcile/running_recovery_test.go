package reconcile

import (
	"context"
	"errors"
	"testing"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

type runningRecoveryStore struct {
	*reconcileStore
}

func (s *runningRecoveryStore) RecordHealthResult(_ context.Context, update store.HealthResultUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "store-health")
	if update.ExpectedRevision != s.record.Revision || s.record.DesiredState != domain.DesiredRunning || s.record.ObservedState != domain.StateRunning {
		return domain.Sandbox{}, domain.ErrConflict
	}
	if update.Healthy {
		s.record.HealthFailureCount = 0
	} else {
		s.record.HealthFailureCount++
	}
	s.record.Revision++
	return s.record, nil
}

type runningRecoveryRuntime struct {
	events        *[]string
	actual        runtimeport.ActualSandbox
	ensureActual  runtimeport.ActualSandbox
	replaceActual runtimeport.ActualSandbox
	egressErrors  []error
	allowDelete   bool
}

func (r *runningRecoveryRuntime) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	*r.events = append(*r.events, "runtime-ensure")
	return r.ensureActual, nil
}

func (r *runningRecoveryRuntime) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	*r.events = append(*r.events, "runtime-inspect")
	return r.actual, nil
}

func (r *runningRecoveryRuntime) Delete(context.Context, string) error {
	if !r.allowDelete {
		panic("full runtime Delete must not be called by Running recovery")
	}
	*r.events = append(*r.events, "runtime-delete")
	return nil
}

type deleteIntentShutdown struct {
	events *[]string
	store  *runningRecoveryStore
	set    bool
}

func (s *deleteIntentShutdown) Shutdown(context.Context, string) error {
	*s.events = append(*s.events, "runner-shutdown")
	if !s.set {
		s.store.mu.Lock()
		s.store.record.DesiredState = domain.DesiredTerminated
		s.store.record.Revision++
		s.store.mu.Unlock()
		s.set = true
	}
	return nil
}

// TestRecoverRunningHonorsDeleteIntentBeforeReplacement 验证关闭准入后出现的删除意图优先于 compute 重建。
func TestRecoverRunningHonorsDeleteIntentBeforeReplacement(t *testing.T) {
	events := make([]string, 0)
	sandbox := runningRecoverySandbox(true)
	sandboxStore := &runningRecoveryStore{newReconcileStore(&events, sandbox)}
	runtime := &runningRecoveryRuntime{events: &events, allowDelete: true,
		actual:       runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
		egressErrors: []error{errors.New("egress unhealthy")},
	}
	shutdown := &deleteIntentShutdown{events: &events, store: sandboxStore}
	reconciler := NewWithShutdown(sandboxStore, runtime,
		&recordingProbe{events: &events, networkIdentity: "net:[42]"}, shutdown)
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	assertEventPresent(t, events, "runtime-delete")
	assertEventAbsent(t, events, "runtime-replace-compute")
}

func (*runningRecoveryRuntime) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}

func (r *runningRecoveryRuntime) ReplaceCompute(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	*r.events = append(*r.events, "runtime-replace-compute")
	return r.replaceActual, nil
}

func (r *runningRecoveryRuntime) CheckSandboxEgress(context.Context, string, string) error {
	*r.events = append(*r.events, "runtime-egress-gate")
	if len(r.egressErrors) == 0 {
		return nil
	}
	err := r.egressErrors[0]
	r.egressErrors = r.egressErrors[1:]
	return err
}

// TestRecoverRunningNetworkNoneEnsuresMissingCompute 验证 network=none 缺失或停止时复用 Ensure 而不是完整替换。
func TestRecoverRunningNetworkNoneEnsuresMissingCompute(t *testing.T) {
	events := make([]string, 0)
	sandbox := runningRecoverySandbox(false)
	sandboxStore := &runningRecoveryStore{newReconcileStore(&events, sandbox)}
	runtime := &runningRecoveryRuntime{events: &events,
		actual:       runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualMissing},
		ensureActual: runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
	}
	reconciler := New(sandboxStore, runtime, &recordingProbe{events: &events})
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	assertEventPresent(t, events, "runtime-ensure")
	assertEventAbsent(t, events, "runtime-replace-compute")
}

// TestRecoverRunningOutboundReplacesAggregateOnEgressFailure 验证 outbound 任一隔离校验失败都会关准入并聚合替换。
func TestRecoverRunningOutboundReplacesAggregateOnEgressFailure(t *testing.T) {
	events := make([]string, 0)
	sandbox := runningRecoverySandbox(true)
	sandboxStore := &runningRecoveryStore{newReconcileStore(&events, sandbox)}
	runtime := &runningRecoveryRuntime{events: &events,
		actual:        runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
		replaceActual: runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
		egressErrors:  []error{errors.New("attestation mismatch"), nil},
	}
	reconciler := NewWithShutdown(sandboxStore, runtime,
		&recordingProbe{events: &events, networkIdentity: "net:[42]"}, &recordingShutdown{events: &events})
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, events, "runner-shutdown", "runtime-replace-compute")
}

// TestRecoverRunningRunnerFailureThreshold 验证前两次仅计数，第三次才关闭准入并替换 compute。
func TestRecoverRunningRunnerFailureThreshold(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		initial     uint32
		wantReplace bool
	}{{"first", 0, false}, {"third", 2, true}} {
		t.Run(testCase.name, func(t *testing.T) {
			events := make([]string, 0)
			sandbox := runningRecoverySandbox(false)
			sandbox.HealthFailureCount = testCase.initial
			sandboxStore := &runningRecoveryStore{newReconcileStore(&events, sandbox)}
			runtime := &runningRecoveryRuntime{events: &events,
				actual:        runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
				replaceActual: runtimeport.ActualSandbox{ID: sandbox.ID, State: runtimeport.ActualRunning, SpecHash: sandbox.SpecHash, RunnerProtocolVersion: 1},
			}
			probe := &sequenceProbe{events: &events, errors: []error{errors.New("runner unavailable"), nil}}
			reconciler := NewWithShutdown(sandboxStore, runtime, probe, &recordingShutdown{events: &events})
			if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
				t.Fatal(err)
			}
			if hasEvent(events, "runtime-replace-compute") != testCase.wantReplace {
				t.Fatalf("events: %v", events)
			}
		})
	}
}

type sequenceProbe struct {
	events *[]string
	errors []error
}

func (p *sequenceProbe) Probe(context.Context, string, int) error {
	*p.events = append(*p.events, "runner-probe")
	if len(p.errors) == 0 {
		return nil
	}
	err := p.errors[0]
	p.errors = p.errors[1:]
	return err
}

func runningRecoverySandbox(outbound bool) domain.Sandbox {
	sandbox := pendingSandbox()
	sandbox.ObservedState = domain.StateRunning
	sandbox.Spec.Network.Outbound = outbound
	sandbox.SpecHash = sandbox.Spec.Hash()
	return sandbox
}

func hasEvent(events []string, target string) bool {
	for _, event := range events {
		if event == target {
			return true
		}
	}
	return false
}

func assertEventPresent(t *testing.T, events []string, target string) {
	t.Helper()
	if !hasEvent(events, target) {
		t.Fatalf("missing %s in %v", target, events)
	}
}

func assertEventAbsent(t *testing.T, events []string, target string) {
	t.Helper()
	if hasEvent(events, target) {
		t.Fatalf("unexpected %s in %v", target, events)
	}
}

func assertOrder(t *testing.T, events []string, before, after string) {
	t.Helper()
	left, right := -1, -1
	for index, event := range events {
		if event == before && left == -1 {
			left = index
		}
		if event == after && right == -1 {
			right = index
		}
	}
	if left == -1 || right == -1 || left >= right {
		t.Fatalf("order %s before %s not satisfied: %v", before, after, events)
	}
}

var _ runtimeport.Runtime = (*runningRecoveryRuntime)(nil)
var _ runtimeport.ComputeReplacer = (*runningRecoveryRuntime)(nil)
