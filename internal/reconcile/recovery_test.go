package reconcile

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

// TestRecoveryAssociatesStoredAndDockerRuntime 验证匹配资源恢复 runtime ID 并入队。
func TestRecoveryAssociatesStoredAndDockerRuntime(t *testing.T) {
	sandbox := recoverySandbox("sandbox-a", domain.DesiredRunning)
	future := time.Now().UTC().Add(time.Hour)
	sandbox.NextReconcileAt = &future
	sandboxStore := newRecoveryStore(sandbox)
	runtime := &recoveryRuntime{managed: []runtimeport.ActualSandbox{
		recoveryActual(sandbox, "container-a"),
	}}
	queue := NewWakeQueue()
	readiness := newRecoveryReadiness()
	service := mustRecoveryService(
		t,
		sandboxStore,
		runtime,
		queue,
		readiness,
		nil,
	)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run recovery: %v", err)
	}
	if got := sandboxStore.records[0].RuntimeID; got != "container-a" {
		t.Fatalf("runtime ID: got %q, want container-a", got)
	}
	if got := sandboxStore.records[0].NextReconcileAt; got == nil || !got.Before(future) {
		t.Fatalf("recovery correction did not advance retry: %#v", got)
	}
	if sandboxStore.candidateCalls != 1 {
		t.Fatalf("candidate scans: got %d, want 1", sandboxStore.candidateCalls)
	}
	if got := nextQueueID(t, queue); got != sandbox.ID {
		t.Fatalf("queued ID: %s", got)
	}
	assertRecoveryReady(t, readiness)
}

// TestRecoveryQueuesStoredSandboxesWithoutDocker 验证两种 desired state 缺容器时均入队。
func TestRecoveryQueuesStoredSandboxesWithoutDocker(t *testing.T) {
	running := recoverySandbox("sandbox-running", domain.DesiredRunning)
	terminated := recoverySandbox(
		"sandbox-terminated",
		domain.DesiredTerminated,
	)
	sandboxStore := newRecoveryStore(running, terminated)
	queue := NewWakeQueue()
	readiness := newRecoveryReadiness()
	service := mustRecoveryService(
		t,
		sandboxStore,
		&recoveryRuntime{},
		queue,
		readiness,
		nil,
	)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run recovery: %v", err)
	}
	got := []string{nextQueueID(t, queue)}
	queue.Done(got[0])
	got = append(got, nextQueueID(t, queue))
	if want := []string{running.ID, terminated.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queued IDs: got %v, want %v", got, want)
	}
	assertRecoveryReady(t, readiness)
}

// TestRecoveryReportsOrphanWithoutImportOrDelete 验证 Docker-only 资源只产生告警。
func TestRecoveryReportsOrphanWithoutImportOrDelete(t *testing.T) {
	orphan := runtimeport.ActualSandbox{
		ID:        "sandbox-orphan",
		RuntimeID: "container-orphan",
		State:     runtimeport.ActualRunning,
		SpecHash:  "orphan-hash",
	}
	runtime := &recoveryRuntime{managed: []runtimeport.ActualSandbox{orphan}}
	diagnostics := make([]RecoveryDiagnostic, 0, 1)
	queue := NewWakeQueue()
	service := mustRecoveryService(
		t,
		newRecoveryStore(),
		runtime,
		queue,
		newRecoveryReadiness(),
		func(diagnostic RecoveryDiagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		},
	)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run recovery: %v", err)
	}
	want := []RecoveryDiagnostic{{
		Code:      RecoveryIssueOrphanRuntime,
		SandboxID: orphan.ID,
	}}
	if !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("diagnostics: got %#v, want %#v", diagnostics, want)
	}
	if runtime.deleteCalls != 0 {
		t.Fatalf("orphan was deleted: %d calls", runtime.deleteCalls)
	}
	if queue.Len() != 0 {
		t.Fatalf("orphan was imported into wake queue: %d", queue.Len())
	}
}

// TestRecoveryReportsDamagedLabelsAndTreatsRuntimeAsMissing 验证损坏资源不参与关联。
func TestRecoveryReportsDamagedLabelsAndTreatsRuntimeAsMissing(t *testing.T) {
	sandbox := recoverySandbox("sandbox-a", domain.DesiredRunning)
	runtime := &recoveryRuntime{managed: []runtimeport.ActualSandbox{{
		ID:             sandbox.ID,
		RuntimeID:      "container-damaged",
		DiscoveryIssue: runtimeport.DiscoveryLabelsInvalid,
	}}}
	queue := NewWakeQueue()
	var diagnostics []RecoveryDiagnostic
	service := mustRecoveryService(
		t,
		newRecoveryStore(sandbox),
		runtime,
		queue,
		newRecoveryReadiness(),
		func(diagnostic RecoveryDiagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		},
	)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run recovery: %v", err)
	}
	if got := nextQueueID(t, queue); got != sandbox.ID {
		t.Fatalf("queued ID: %s", got)
	}
	want := []RecoveryDiagnostic{{
		Code:      runtimeport.DiscoveryLabelsInvalid,
		SandboxID: sandbox.ID,
	}}
	if !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("diagnostics: got %#v, want %#v", diagnostics, want)
	}
}

// TestRecoveryReportsSpecDriftAndKeepsResourceRecoverable 验证漂移资源恢复 ID 后交给状态机。
func TestRecoveryReportsSpecDriftAndKeepsResourceRecoverable(t *testing.T) {
	sandbox := recoverySandbox("sandbox-a", domain.DesiredRunning)
	actual := recoveryActual(sandbox, "container-drifted")
	actual.SpecHash = "different-hash"
	queue := NewWakeQueue()
	sandboxStore := newRecoveryStore(sandbox)
	var diagnostics []RecoveryDiagnostic
	service := mustRecoveryService(
		t,
		sandboxStore,
		&recoveryRuntime{managed: []runtimeport.ActualSandbox{actual}},
		queue,
		newRecoveryReadiness(),
		func(diagnostic RecoveryDiagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		},
	)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run recovery: %v", err)
	}
	if sandboxStore.records[0].RuntimeID != actual.RuntimeID {
		t.Fatalf("drifted runtime ID was not recovered: %#v", sandboxStore.records[0])
	}
	if got := nextQueueID(t, queue); got != sandbox.ID {
		t.Fatalf("queued ID: %s", got)
	}
	want := []RecoveryDiagnostic{{
		Code:      RecoveryIssueSpecDrift,
		SandboxID: sandbox.ID,
	}}
	if !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("diagnostics: got %#v, want %#v", diagnostics, want)
	}
}

// TestRecoveryFailureNeverMarksReady 验证扫描或 CAS 失败时 readiness 保持 false。
func TestRecoveryFailureNeverMarksReady(t *testing.T) {
	cause := errors.New("recovery failure")
	tests := []struct {
		name    string
		store   *recoveryStore
		runtime *recoveryRuntime
	}{
		{
			name:    "runtime list",
			store:   newRecoveryStore(),
			runtime: &recoveryRuntime{listErr: cause},
		},
		{
			name: "store list all",
			store: &recoveryStore{
				listAllErr: cause,
			},
			runtime: &recoveryRuntime{},
		},
		{
			name: "candidate list",
			store: &recoveryStore{
				candidatesErr: cause,
			},
			runtime: &recoveryRuntime{},
		},
		{
			name: "runtime ID CAS",
			store: &recoveryStore{
				records:   []domain.Sandbox{recoverySandbox("sandbox-a", domain.DesiredRunning)},
				updateErr: cause,
			},
			runtime: &recoveryRuntime{managed: []runtimeport.ActualSandbox{{
				ID:        "sandbox-a",
				RuntimeID: "container-a",
				SpecHash:  "spec-hash",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readiness := newRecoveryReadiness()
			service := mustRecoveryService(
				t,
				tt.store,
				tt.runtime,
				NewWakeQueue(),
				readiness,
				nil,
			)
			if err := service.Run(context.Background()); !errors.Is(err, cause) {
				t.Fatalf("recovery error: %v", err)
			}
			if readiness.ready() {
				t.Fatal("failed recovery marked readiness true")
			}
		})
	}
}

// TestNewRecoveryServiceRejectsMissingDependencies 验证恢复不会以残缺依赖启动。
func TestNewRecoveryServiceRejectsMissingDependencies(t *testing.T) {
	validStore := newRecoveryStore()
	validRuntime := &recoveryRuntime{}
	validQueue := NewWakeQueue()
	validReadiness := newRecoveryReadiness()
	tests := []struct {
		name      string
		store     store.Store
		runtime   runtimeport.Runtime
		queue     *WakeQueue
		readiness RecoveryReadiness
	}{
		{"nil store", nil, validRuntime, validQueue, validReadiness},
		{"nil runtime", validStore, nil, validQueue, validReadiness},
		{"nil queue", validStore, validRuntime, nil, validReadiness},
		{"nil readiness", validStore, validRuntime, validQueue, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRecoveryService(
				tt.store,
				tt.runtime,
				tt.queue,
				tt.readiness,
				nil,
			); err == nil {
				t.Fatal("incomplete recovery service was accepted")
			}
		})
	}
}

// recoveryStore 是支持 ListAll、candidate 和 runtime ID CAS 的恢复测试 Store。
type recoveryStore struct {
	mu             sync.Mutex
	records        []domain.Sandbox
	listAllErr     error
	candidatesErr  error
	updateErr      error
	candidateCalls int
}

// newRecoveryStore 创建以全部记录作为 candidate 的测试 Store。
func newRecoveryStore(records ...domain.Sandbox) *recoveryStore {
	return &recoveryStore{records: append([]domain.Sandbox(nil), records...)}
}

// Create 未被恢复服务使用。
func (*recoveryStore) Create(context.Context, domain.Sandbox) error {
	return errors.New("unexpected Create")
}

// Get 未被恢复服务使用。
func (*recoveryStore) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected Get")
}

// UpdateDesired 未被恢复服务使用。
func (*recoveryStore) UpdateDesired(
	context.Context,
	string,
	domain.DesiredState,
	uint64,
) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected UpdateDesired")
}

// UpdateObserved 按 ID 和 revision 恢复 runtime ID。
func (s *recoveryStore) UpdateObserved(
	_ context.Context,
	update store.ObservedUpdate,
) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return domain.Sandbox{}, s.updateErr
	}
	for index := range s.records {
		if s.records[index].ID != update.ID {
			continue
		}
		if s.records[index].Revision != update.ExpectedRevision {
			return domain.Sandbox{}, domain.ErrConflict
		}
		s.records[index].RuntimeID = update.RuntimeID
		if update.ReconcileAt != nil {
			reconcileAt := update.ReconcileAt.UTC()
			s.records[index].NextReconcileAt = &reconcileAt
		}
		s.records[index].Revision++
		return s.records[index], nil
	}
	return domain.Sandbox{}, domain.ErrNotFound
}

// ListReconcileCandidates 返回 limit 内全部测试记录。
func (s *recoveryStore) ListReconcileCandidates(
	_ context.Context,
	query store.ReconcileCandidateQuery,
) ([]domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidateCalls++
	if s.candidatesErr != nil {
		return nil, s.candidatesErr
	}
	limit := query.Limit
	if limit > len(s.records) {
		limit = len(s.records)
	}
	return append([]domain.Sandbox(nil), s.records[:limit]...), nil
}

// CreateIdempotent 未被启动恢复测试使用。
func (s *recoveryStore) CreateIdempotent(context.Context, store.IdempotentCreateRequest) (store.IdempotentCreateResult, error) {
	return store.IdempotentCreateResult{}, errors.New("unexpected CreateIdempotent")
}

// CreateNonIdempotent 未被启动恢复测试使用。
func (s *recoveryStore) CreateNonIdempotent(context.Context, store.NonIdempotentCreateRequest) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected CreateNonIdempotent")
}

// Renew 未被启动恢复测试使用。
func (s *recoveryStore) Renew(context.Context, store.RenewUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected Renew")
}

// ExpireIntent 未被启动恢复测试使用。
func (s *recoveryStore) ExpireIntent(context.Context, store.ExpireIntentUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected ExpireIntent")
}

// ScheduleRetry 未被启动恢复测试使用。
func (s *recoveryStore) ScheduleRetry(context.Context, store.RetryUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected ScheduleRetry")
}

// ResetRetry 未被启动恢复测试使用。
func (s *recoveryStore) ResetRetry(context.Context, store.RetryResetUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected ResetRetry")
}

// RecordHealthResult 未被启动恢复测试使用。
func (s *recoveryStore) RecordHealthResult(context.Context, store.HealthResultUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected RecordHealthResult")
}

// ListAll 返回全部测试记录。
func (s *recoveryStore) ListAll(
	context.Context,
) ([]domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listAllErr != nil {
		return nil, s.listAllErr
	}
	return append([]domain.Sandbox(nil), s.records...), nil
}

// recoveryRuntime 是只实现 ListManaged 的恢复测试 Runtime。
type recoveryRuntime struct {
	managed     []runtimeport.ActualSandbox
	listErr     error
	deleteCalls int
}

// Ensure 未被恢复服务使用。
func (*recoveryRuntime) Ensure(
	context.Context,
	domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected Ensure")
}

// Inspect 未被恢复服务使用。
func (*recoveryRuntime) Inspect(
	context.Context,
	string,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected Inspect")
}

// Delete 记录意外的 orphan 删除尝试。
func (r *recoveryRuntime) Delete(context.Context, string) error {
	r.deleteCalls++
	return errors.New("unexpected Delete")
}

// ListManaged 返回预设 Docker 扫描结果。
func (r *recoveryRuntime) ListManaged(
	context.Context,
) ([]runtimeport.ActualSandbox, error) {
	return append([]runtimeport.ActualSandbox(nil), r.managed...), r.listErr
}

// recoveryReadiness 记录恢复状态变更。
type recoveryReadiness struct {
	mu     sync.Mutex
	values []bool
}

// newRecoveryReadiness 创建初始值为 true 的替身，验证 Run 会先重置 false。
func newRecoveryReadiness() *recoveryReadiness {
	return &recoveryReadiness{values: []bool{true}}
}

// SetRecovery 记录 recovery readiness。
func (r *recoveryReadiness) SetRecovery(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, ready)
}

// ready 返回最近状态。
func (r *recoveryReadiness) ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[len(r.values)-1]
}

// recoverySandbox 创建恢复用持久化记录。
func recoverySandbox(id string, desired domain.DesiredState) domain.Sandbox {
	return domain.Sandbox{
		ID:            id,
		DesiredState:  desired,
		ObservedState: domain.StateCreating,
		Reason:        reasonCreatingRuntime,
		Message:       messageCreatingRuntime,
		SpecHash:      "spec-hash",
		Revision:      1,
	}
}

// recoveryActual 创建与 Store 规格匹配的 runtime 观测。
func recoveryActual(
	sandbox domain.Sandbox,
	runtimeID string,
) runtimeport.ActualSandbox {
	return runtimeport.ActualSandbox{
		ID:        sandbox.ID,
		RuntimeID: runtimeID,
		State:     runtimeport.ActualRunning,
		SpecHash:  sandbox.SpecHash,
	}
}

// mustRecoveryService 创建测试恢复服务。
func mustRecoveryService(
	t *testing.T,
	s store.Store,
	runtime runtimeport.Runtime,
	queue *WakeQueue,
	readiness RecoveryReadiness,
	report RecoveryReporter,
) *RecoveryService {
	t.Helper()
	service, err := NewRecoveryService(
		s,
		runtime,
		queue,
		readiness,
		report,
	)
	if err != nil {
		t.Fatalf("new recovery service: %v", err)
	}
	return service
}

// assertRecoveryReady 验证状态严格经历 false 后才变为 true。
func assertRecoveryReady(t *testing.T, readiness *recoveryReadiness) {
	t.Helper()
	readiness.mu.Lock()
	defer readiness.mu.Unlock()
	want := []bool{true, false, true}
	if !reflect.DeepEqual(readiness.values, want) {
		t.Fatalf("readiness transitions: got %v, want %v", readiness.values, want)
	}
}

var _ store.Store = (*recoveryStore)(nil)
var _ runtimeport.Runtime = (*recoveryRuntime)(nil)
var _ RecoveryReadiness = (*recoveryReadiness)(nil)
