package reconcile

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

// TestReconcileRunningTransitionsInOrder 验证创建路径的 CAS、Runtime 和 Probe 顺序。
func TestReconcileRunningTransitionsInOrder(t *testing.T) {
	events := make([]string, 0, 7)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	runtime := &recordingRuntime{
		events: &events,
		ensureResult: runtimeport.ActualSandbox{
			ID:        "sandbox-id",
			RuntimeID: "container-id",
			State:     runtimeport.ActualRunning,
		},
	}
	probe := &recordingProbe{events: &events}
	reconciler := New(sandboxStore, runtime, probe)

	if err := reconciler.Reconcile(
		context.Background(),
		"sandbox-id",
	); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	wantEvents := []string{
		"store-get",
		"store-update-Creating-CREATING_RUNTIME",
		"runtime-ensure",
		"store-update-Creating-WAITING_RUNNER",
		"runner-probe",
		"store-update-Running-RUNNING",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events:\n got %v\nwant %v", events, wantEvents)
	}
	updates := sandboxStore.updatesSnapshot()
	if len(updates) != 3 ||
		updates[0].ExpectedRevision != 7 ||
		updates[1].ExpectedRevision != 8 ||
		updates[2].ExpectedRevision != 9 ||
		updates[1].RuntimeID != "container-id" ||
		updates[2].RuntimeID != "container-id" {
		t.Fatalf("updates: %#v", updates)
	}
	if got := runtime.ensureSandbox.ObservedState; got != domain.StateCreating {
		t.Fatalf("Ensure sandbox state: got %s", got)
	}
}

// TestReconcileRunningRecordIsNoOp 验证已 Running 记录不会触发 Runtime、Probe 或 CAS。
func TestReconcileRunningRecordIsNoOp(t *testing.T) {
	events := make([]string, 0, 1)
	sandbox := pendingSandbox()
	sandbox.ObservedState = domain.StateRunning
	sandbox.Reason = reasonRunning
	sandboxStore := newReconcileStore(&events, sandbox)
	reconciler := New(
		sandboxStore,
		&recordingRuntime{events: &events},
		&recordingProbe{events: &events},
	)

	if err := reconciler.Reconcile(
		context.Background(),
		sandbox.ID,
	); err != nil {
		t.Fatalf("reconcile running: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"store-get"}) {
		t.Fatalf("events: %v", events)
	}
}

// TestReconcileRunningStopsAtRuntimeFailure 验证 Ensure 失败时停留在 Creating。
func TestReconcileRunningStopsAtRuntimeFailure(t *testing.T) {
	events := make([]string, 0, 3)
	cause := errors.New("runtime failure")
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	reconciler := New(
		sandboxStore,
		&recordingRuntime{events: &events, ensureErr: cause},
		&recordingProbe{events: &events},
	)

	err := reconciler.Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, cause) {
		t.Fatalf("error: got %v, want runtime cause", err)
	}
	want := []string{
		"store-get",
		"store-update-Creating-CREATING_RUNTIME",
		"runtime-ensure",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v, want %v", events, want)
	}
}

// TestReconcileRunningStopsAtProbeFailure 验证 Probe 失败时不提前写 Running。
func TestReconcileRunningStopsAtProbeFailure(t *testing.T) {
	events := make([]string, 0, 5)
	cause := errors.New("probe failure")
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	reconciler := New(
		sandboxStore,
		&recordingRuntime{
			events: &events,
			ensureResult: runtimeport.ActualSandbox{
				RuntimeID: "container-id",
			},
		},
		&recordingProbe{events: &events, err: cause},
	)

	err := reconciler.Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, cause) {
		t.Fatalf("error: got %v, want probe cause", err)
	}
	want := []string{
		"store-get",
		"store-update-Creating-CREATING_RUNTIME",
		"runtime-ensure",
		"store-update-Creating-WAITING_RUNNER",
		"runner-probe",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v, want %v", events, want)
	}
}

// reconcileStore 是维护 revision CAS 的最小状态化 Store fake。
type reconcileStore struct {
	mu      sync.Mutex
	record  domain.Sandbox
	events  *[]string
	updates []store.ObservedUpdate
}

// newReconcileStore 创建包含单条记录的状态化 Store fake。
func newReconcileStore(
	events *[]string,
	record domain.Sandbox,
) *reconcileStore {
	return &reconcileStore{events: events, record: record}
}

// Create 未被 reconciler 使用。
func (s *reconcileStore) Create(context.Context, domain.Sandbox) error {
	return errors.New("unexpected Create")
}

// Get 返回最新记录并记录锁内重读。
func (s *reconcileStore) Get(
	_ context.Context,
	id string,
) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "store-get")
	if id != s.record.ID {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	return s.record, nil
}

// UpdateDesired 未被当前状态路径使用。
func (s *reconcileStore) UpdateDesired(
	context.Context,
	string,
	domain.DesiredState,
	uint64,
) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected UpdateDesired")
}

// UpdateObserved 校验 revision、更新记录并返回递增后的 snapshot。
func (s *reconcileStore) UpdateObserved(
	_ context.Context,
	update store.ObservedUpdate,
) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.ID != s.record.ID ||
		update.ExpectedRevision != s.record.Revision {
		return domain.Sandbox{}, domain.ErrConflict
	}
	s.updates = append(s.updates, update)
	*s.events = append(
		*s.events,
		"store-update-"+string(update.State)+"-"+update.Reason,
	)
	s.record.ObservedState = update.State
	s.record.Reason = update.Reason
	s.record.Message = update.Message
	s.record.RuntimeID = update.RuntimeID
	s.record.Revision++
	return s.record, nil
}

// ListReconcileCandidates 未被单 ID reconcile 使用。
func (s *reconcileStore) ListReconcileCandidates(
	context.Context,
	int,
) ([]domain.Sandbox, error) {
	return nil, errors.New("unexpected ListReconcileCandidates")
}

// ListAll 未被单 ID reconcile 使用。
func (s *reconcileStore) ListAll(context.Context) ([]domain.Sandbox, error) {
	return nil, errors.New("unexpected ListAll")
}

// updatesSnapshot 返回 CAS 参数副本。
func (s *reconcileStore) updatesSnapshot() []store.ObservedUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.ObservedUpdate(nil), s.updates...)
}

// recordingRuntime 是记录 Ensure/Delete 顺序的 Runtime fake。
type recordingRuntime struct {
	events        *[]string
	ensureResult  runtimeport.ActualSandbox
	ensureErr     error
	ensureSandbox domain.Sandbox
	deleteErr     error
}

// Ensure 记录调用并返回预设实际状态。
func (r *recordingRuntime) Ensure(
	_ context.Context,
	sandbox domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	*r.events = append(*r.events, "runtime-ensure")
	r.ensureSandbox = sandbox
	return r.ensureResult, r.ensureErr
}

// Inspect 未被 reconciler 直接使用。
func (*recordingRuntime) Inspect(
	context.Context,
	string,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected Inspect")
}

// Delete 记录调用并返回预设错误。
func (r *recordingRuntime) Delete(context.Context, string) error {
	*r.events = append(*r.events, "runtime-delete")
	return r.deleteErr
}

// ListManaged 未被单 ID reconcile 使用。
func (*recordingRuntime) ListManaged(
	context.Context,
) ([]runtimeport.ActualSandbox, error) {
	return nil, errors.New("unexpected ListManaged")
}

// recordingProbe 记录健康检查顺序。
type recordingProbe struct {
	events *[]string
	err    error
}

// Probe 记录调用并返回预设错误。
func (p *recordingProbe) Probe(context.Context, string) error {
	*p.events = append(*p.events, "runner-probe")
	return p.err
}

// pendingSandbox 返回 revision 已存在的 DesiredRunning 记录。
func pendingSandbox() domain.Sandbox {
	return domain.Sandbox{
		ID:            "sandbox-id",
		DesiredState:  domain.DesiredRunning,
		ObservedState: domain.StatePending,
		Revision:      7,
	}
}

var _ store.Store = (*reconcileStore)(nil)
var _ runtimeport.Runtime = (*recordingRuntime)(nil)
var _ RunnerProbe = (*recordingProbe)(nil)
