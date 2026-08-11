package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/internal/store"
)

// TestReconcileRunningTransitionsInOrder 验证创建路径的 CAS、Runtime 和 Probe 顺序。
func TestReconcileRunningTransitionsInOrder(t *testing.T) {
	events := make([]string, 0, 7)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	runtime := &recordingRuntime{
		events: &events,
		ensureResult: runtimeport.ActualSandbox{
			ID:                    "sandbox-id",
			RuntimeID:             "container-id",
			State:                 runtimeport.ActualRunning,
			RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
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
	if got := probe.protocolVersion; got != runnerbootstrap.CurrentProtocolVersion {
		t.Fatalf("probe protocol version: got %d", got)
	}
}

// TestReconcileOutboundRequiresRunnerAndEgressIdentity 验证 outbound 只有双重就绪后才能进入 Running。
func TestReconcileOutboundRequiresRunnerAndEgressIdentity(t *testing.T) {
	events := make([]string, 0, 8)
	sandbox := pendingSandbox()
	sandbox.Spec.Network.Outbound = true
	sandboxStore := newReconcileStore(&events, sandbox)
	runtime := &recordingRuntime{events: &events, ensureResult: runtimeport.ActualSandbox{RuntimeID: "container-id", RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion}}
	probe := &recordingProbe{events: &events, networkIdentity: "linux-netns:4:9"}
	if err := New(sandboxStore, runtime, probe).Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("reconcile outbound: %v", err)
	}
	want := []string{"store-get", "store-update-Creating-CREATING_RUNTIME", "runtime-ensure", "store-update-Creating-WAITING_RUNNER", "runner-network-probe", "runtime-egress-gate", "store-update-Running-RUNNING"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v want %v", events, want)
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

// TestReconcileSettledStatesResetRetryMetadata 验证 Running 与 Terminated 的最终成功
// 更新会原子清零旧 retry，而已清零记录保持幂等 no-op。
func TestReconcileSettledStatesResetRetryMetadata(t *testing.T) {
	now := time.Date(2027, 5, 7, 8, 9, 10, 0, time.UTC)
	for _, testCase := range []struct {
		name     string
		desired  domain.DesiredState
		observed domain.SandboxState
		reason   string
	}{
		{name: "running", desired: domain.DesiredRunning, observed: domain.StateRunning, reason: reasonRunning},
		{name: "terminated", desired: domain.DesiredTerminated, observed: domain.StateTerminated, reason: reasonTerminated},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			events := make([]string, 0, 2)
			sandbox := pendingSandbox()
			sandbox.DesiredState, sandbox.ObservedState = testCase.desired, testCase.observed
			sandbox.RetryAttempt = 4
			next := now.Add(time.Hour)
			sandbox.NextReconcileAt = &next
			sandboxStore := newReconcileStore(&events, sandbox)
			reconciler, err := NewWithRetry(sandboxStore, &recordingRuntime{events: &events}, &recordingProbe{events: &events}, &manualClock{now: now}, fixedRandom{}, time.Second, time.Minute)
			if err != nil {
				t.Fatalf("new reconciler: %v", err)
			}
			if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if sandboxStore.record.RetryAttempt != 0 || sandboxStore.record.NextReconcileAt != nil || len(sandboxStore.resetCalls) != 1 {
				t.Fatalf("retry metadata was not reset: %#v", sandboxStore.record)
			}
			if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
				t.Fatalf("idempotent reconcile: %v", err)
			}
			if len(sandboxStore.resetCalls) != 1 {
				t.Fatalf("already reset record was rewritten: %#v", sandboxStore.resetCalls)
			}
		})
	}
}

// TestReconcileIntermediateCreatingDoesNotResetRetry 验证尚未到达 Running 前不会提前清零。
func TestReconcileIntermediateCreatingDoesNotResetRetry(t *testing.T) {
	events := make([]string, 0, 3)
	sandbox := pendingSandbox()
	sandbox.RetryAttempt = 3
	next := time.Now().UTC().Add(time.Hour)
	sandbox.NextReconcileAt = &next
	sandboxStore := newReconcileStore(&events, sandbox)
	sandboxStore.failReason, sandboxStore.failErr = reasonWaitingRunner, errors.New("store unavailable")
	err := New(
		sandboxStore,
		&recordingRuntime{events: &events, ensureResult: runtimeport.ActualSandbox{RuntimeID: "container-id"}},
		&recordingProbe{events: &events},
	).Reconcile(context.Background(), sandbox.ID)
	if err == nil {
		t.Fatal("reconcile unexpectedly succeeded")
	}
	if sandboxStore.record.RetryAttempt != 3 || sandboxStore.record.NextReconcileAt == nil || len(sandboxStore.resetCalls) != 0 {
		t.Fatalf("intermediate update cleared retry: %#v", sandboxStore.record)
	}
}

// TestReconcileFinalResetHonorsCAS 验证最终成功不能覆盖并发写入的新 revision。
func TestReconcileFinalResetHonorsCAS(t *testing.T) {
	events := make([]string, 0, 6)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	sandboxStore.failReason, sandboxStore.failErr = reasonRunning, domain.ErrConflict
	err := New(
		sandboxStore,
		&recordingRuntime{events: &events, ensureResult: runtimeport.ActualSandbox{RuntimeID: "container-id"}},
		&recordingProbe{events: &events},
	).Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("final reset error: got %v, want conflict", err)
	}
	if sandboxStore.record.ObservedState != domain.StateCreating || sandboxStore.record.Reason != reasonWaitingRunner {
		t.Fatalf("conflicting final state was written: %#v", sandboxStore.record)
	}
}

// TestReconcileRunningRecordsRuntimeFailure 验证 Ensure 失败后清理并写入 Failed。
func TestReconcileRunningRecordsRuntimeFailure(t *testing.T) {
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
		"runtime-delete",
		"store-update-Failed-INTERNAL_ERROR",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v, want %v", events, want)
	}
}

// TestReconcileRunningRecordsProbeFailure 验证协议不匹配时不能进入 Running，
// 并按不可重试的稳定 reason 清理后写入 Failed。
func TestReconcileRunningRecordsProbeFailure(t *testing.T) {
	events := make([]string, 0, 5)
	cause := &runnerclient.ProtocolMismatchError{}
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	reconciler := New(
		sandboxStore,
		&recordingRuntime{
			events: &events,
			ensureResult: runtimeport.ActualSandbox{
				RuntimeID:             "container-id",
				RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
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
		"runtime-delete",
		"store-update-Failed-RUNNER_PROTOCOL_MISMATCH",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v, want %v", events, want)
	}
	if len(sandboxStore.retryCalls) != 0 {
		t.Fatalf("protocol mismatch must not schedule retry: %#v", sandboxStore.retryCalls)
	}
}

// TestReconcileTerminatedTransitionsFromEveryNonTerminalState 验证所有起点先 Stopping 再删除。
func TestReconcileTerminatedTransitionsFromEveryNonTerminalState(t *testing.T) {
	for _, state := range []domain.SandboxState{
		domain.StatePending,
		domain.StateCreating,
		domain.StateRunning,
		domain.StateFailed,
		domain.StateStopping,
	} {
		t.Run(string(state), func(t *testing.T) {
			events := make([]string, 0, 4)
			sandbox := pendingSandbox()
			sandbox.DesiredState = domain.DesiredTerminated
			sandbox.ObservedState = state
			sandbox.RuntimeID = "container-id"
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
				t.Fatalf("reconcile terminated: %v", err)
			}
			want := []string{
				"store-get",
				"store-update-Stopping-DELETING_RUNTIME",
				"runtime-delete",
				"store-update-Terminated-TERMINATED",
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events: got %v, want %v", events, want)
			}
			updates := sandboxStore.updatesSnapshot()
			if len(updates) != 2 ||
				updates[0].RuntimeID != "container-id" ||
				updates[1].RuntimeID != "" {
				t.Fatalf("updates: %#v", updates)
			}
		})
	}
}

// TestReconcileTerminatedDeleteFailureWritesCleanupPending 验证删除失败进入可重试清理态。
func TestReconcileTerminatedDeleteFailureWritesCleanupPending(t *testing.T) {
	events := make([]string, 0, 3)
	cause := errors.New("delete failure")
	sandbox := pendingSandbox()
	sandbox.DesiredState = domain.DesiredTerminated
	sandbox.ObservedState = domain.StateRunning
	sandboxStore := newReconcileStore(&events, sandbox)
	reconciler := New(
		sandboxStore,
		&recordingRuntime{events: &events, deleteErr: cause},
		&recordingProbe{events: &events},
	)

	err := reconciler.Reconcile(context.Background(), sandbox.ID)
	if !errors.Is(err, cause) {
		t.Fatalf("error: got %v, want delete cause", err)
	}
	want := []string{
		"store-get",
		"store-update-Stopping-DELETING_RUNTIME",
		"runtime-delete",
		"store-update-Failed-CLEANUP_PENDING",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v, want %v", events, want)
	}
	if got := sandboxStore.record.ObservedState; got != domain.StateFailed {
		t.Fatalf("state: got %s, want Failed", got)
	}
	if sandboxStore.record.RetryAttempt != 1 || sandboxStore.record.NextReconcileAt == nil {
		t.Fatalf("delete retry was not persisted: %#v", sandboxStore.record)
	}
}

// TestFailEgressPreservesDesiredAndClosesAdmission 验证网络隔离漂移只写 observed failure 并关闭 runner。
func TestFailEgressPreservesDesiredAndClosesAdmission(t *testing.T) {
	events := make([]string, 0, 3)
	sandbox := pendingSandbox()
	sandbox.ObservedState = domain.StateRunning
	sandbox.RuntimeID = "container-id"
	sandboxStore := newReconcileStore(&events, sandbox)
	shutdown := &recordingShutdown{events: &events, err: errors.New("runner unavailable")}
	reconciler := NewWithShutdown(sandboxStore, &recordingRuntime{events: &events}, &recordingProbe{events: &events}, shutdown)
	if err := reconciler.FailEgress(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("fail egress: %v", err)
	}
	if sandboxStore.record.DesiredState != domain.DesiredRunning || sandboxStore.record.ObservedState != domain.StateFailed || sandboxStore.record.Reason != reasonEgressUnhealthy {
		t.Fatalf("record: %#v", sandboxStore.record)
	}
	want := []string{"store-get", "store-update-Failed-EGRESS_UNHEALTHY", "runner-shutdown"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v want %v", events, want)
	}
}

// TestReconcileRunningPersistsEveryFailureReason 验证所有 runtime 分类均以安全状态落库。
func TestReconcileRunningPersistsEveryFailureReason(t *testing.T) {
	const secret = "daemon secret detail"
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{"image pull", new(dockerruntime.ImagePullFailedError), runtimeport.FailureReasonImagePullFailed},
		{"artifact invalid", new(dockerruntime.ArtifactInvalidError), runtimeport.FailureReasonArtifactInvalid},
		{"container create", new(dockerruntime.ContainerCreateFailedError), runtimeport.FailureReasonContainerCreateFailed},
		{"artifact injection", new(dockerruntime.ArtifactInjectionFailedError), runtimeport.FailureReasonArtifactInjectionFailed},
		{"container start", new(dockerruntime.ContainerStartFailedError), runtimeport.FailureReasonContainerStartFailed},
		{"runner unhealthy", new(runnerclient.UnhealthyError), runtimeport.FailureReasonRunnerUnhealthy},
		{"spec drift", new(dockerruntime.SpecDriftError), runtimeport.FailureReasonSpecDrift},
		{"cleanup pending", new(dockerruntime.CleanupPendingError), runtimeport.FailureReasonCleanupPending},
		{"runtime unavailable", new(dockerruntime.RuntimeUnavailableError), runtimeport.FailureReasonRuntimeUnavailable},
		{"internal", errors.New("unknown failure"), runtimeport.FailureReasonInternalError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make([]string, 0, 5)
			sandbox := pendingSandbox()
			sandbox.RuntimeID = "stale-runtime-id"
			sandboxStore := newReconcileStore(&events, sandbox)
			operationErr := errors.Join(errors.New(secret), tt.err)
			reconciler := New(
				sandboxStore,
				&recordingRuntime{
					events:    &events,
					ensureErr: operationErr,
				},
				&recordingProbe{events: &events},
			)

			err := reconciler.Reconcile(context.Background(), sandbox.ID)
			if !errors.Is(err, tt.err) {
				t.Fatalf("reconcile lost operation cause: %v", err)
			}
			record := sandboxStore.record
			if record.ObservedState != domain.StateFailed ||
				record.Reason != tt.reason ||
				record.Message == "" ||
				record.RuntimeID != "" {
				t.Fatalf("failed record: %#v", record)
			}
			if strings.Contains(record.Message, secret) {
				t.Fatalf("stored message leaked cause: %q", record.Message)
			}
		})
	}
}

// TestReconcileProbeCleanupFailureKeepsRuntimeForRetry 验证 runner 失败且清理失败时保留定位信息。
func TestReconcileProbeCleanupFailureKeepsRuntimeForRetry(t *testing.T) {
	events := make([]string, 0, 8)
	probeErr := new(runnerclient.UnhealthyError)
	cleanupErr := errors.New("delete failed")
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	reconciler := New(
		sandboxStore,
		&recordingRuntime{
			events: &events,
			ensureResult: runtimeport.ActualSandbox{
				RuntimeID: "container-id",
			},
			deleteErr: cleanupErr,
		},
		&recordingProbe{events: &events, err: probeErr},
	)

	err := reconciler.Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, probeErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("reconcile lost failure causes: %v", err)
	}
	record := sandboxStore.record
	if record.ObservedState != domain.StateFailed ||
		record.Reason != runtimeport.FailureReasonCleanupPending ||
		record.RuntimeID != "container-id" {
		t.Fatalf("cleanup pending record: %#v", record)
	}
}

// TestReconcileSuccessfulDeleteRestoresCompensatedOperationReason 验证二次清理成功后不遗留假清理态。
func TestReconcileSuccessfulDeleteRestoresCompensatedOperationReason(
	t *testing.T,
) {
	events := make([]string, 0, 6)
	operationErr := new(dockerruntime.ImagePullFailedError)
	ensureErr := &compensatedFailure{operationErr: operationErr}
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	reconciler := New(
		sandboxStore,
		&recordingRuntime{events: &events, ensureErr: ensureErr},
		&recordingProbe{events: &events},
	)

	err := reconciler.Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, operationErr) {
		t.Fatalf("reconcile lost original operation cause: %v", err)
	}
	if got := sandboxStore.record.Reason; got !=
		runtimeport.FailureReasonImagePullFailed {
		t.Fatalf("reason: got %s, want image pull failed", got)
	}
}

// TestReconcileFailureCASConflictDoesNotOverwriteState 验证失败落库遇到并发修订时保持原记录。
func TestReconcileFailureCASConflictDoesNotOverwriteState(t *testing.T) {
	events := make([]string, 0, 6)
	operationErr := new(dockerruntime.ImagePullFailedError)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	sandboxStore.failReason = runtimeport.FailureReasonImagePullFailed
	sandboxStore.failErr = domain.ErrConflict
	reconciler := New(
		sandboxStore,
		&recordingRuntime{events: &events, ensureErr: operationErr},
		&recordingProbe{events: &events},
	)

	err := reconciler.Reconcile(context.Background(), "sandbox-id")
	if !errors.Is(err, domain.ErrConflict) ||
		!errors.Is(err, operationErr) {
		t.Fatalf("CAS failure causes: %v", err)
	}
	if sandboxStore.record.ObservedState != domain.StateCreating {
		t.Fatalf("conflicting state was overwritten: %#v", sandboxStore.record)
	}
	if got := events[len(events)-1]; got != "store-get" {
		t.Fatalf("CAS conflict did not reread latest record: %v", events)
	}
}

// TestReconcileRetryScheduleUsesPersistedAttempt 验证首次及后续失败都根据已持久化 attempt
// 计算绝对时间，进程重启不会把退避重置为首次失败。
func TestReconcileRetryScheduleUsesPersistedAttempt(t *testing.T) {
	now := time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)
	for _, testCase := range []struct {
		name        string
		attempt     uint32
		wantAttempt uint32
		wantDelay   time.Duration
	}{
		{name: "first", attempt: 0, wantAttempt: 1, wantDelay: 10 * time.Second},
		{name: "persisted fourth", attempt: 3, wantAttempt: 4, wantDelay: time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			events := make([]string, 0, 5)
			sandbox := pendingSandbox()
			sandbox.RetryAttempt = testCase.attempt
			sandboxStore := newReconcileStore(&events, sandbox)
			reconciler, err := NewWithRetry(
				sandboxStore,
				&recordingRuntime{events: &events, ensureErr: new(dockerruntime.RuntimeUnavailableError)},
				&recordingProbe{events: &events},
				&manualClock{now: now}, fixedRandom{value: int64(testCase.wantDelay) - 1},
				10*time.Second, time.Minute,
			)
			if err != nil {
				t.Fatalf("new reconciler: %v", err)
			}
			if err := reconciler.Reconcile(context.Background(), sandbox.ID); err == nil {
				t.Fatal("reconcile unexpectedly succeeded")
			}
			got := sandboxStore.record
			if got.RetryAttempt != testCase.wantAttempt || got.LastReconcileAt == nil ||
				!got.LastReconcileAt.Equal(now) || got.NextReconcileAt == nil ||
				!got.NextReconcileAt.Equal(now.Add(testCase.wantDelay)) {
				t.Fatalf("retry record: %#v", got)
			}
		})
	}
}

// TestReconcileTerminatedRecordIsNoOp 验证已 Terminated 记录不再调用 Runtime.Delete。
func TestReconcileTerminatedRecordIsNoOp(t *testing.T) {
	events := make([]string, 0, 1)
	sandbox := pendingSandbox()
	sandbox.DesiredState = domain.DesiredTerminated
	sandbox.ObservedState = domain.StateTerminated
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
		t.Fatalf("reconcile terminated record: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"store-get"}) {
		t.Fatalf("events: %v", events)
	}
}

// reconcileStore 是维护 revision CAS 的最小状态化 Store fake。
type reconcileStore struct {
	mu          sync.Mutex
	record      domain.Sandbox
	events      *[]string
	updates     []store.ObservedUpdate
	failReason  string
	failErr     error
	retryCalls  []store.RetryUpdate
	resetCalls  []store.RetryResetUpdate
	expireCalls []store.ExpireIntentUpdate
	expireErr   error
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
	if update.Reason == s.failReason && s.failErr != nil {
		return domain.Sandbox{}, s.failErr
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
	store.ReconcileCandidateQuery,
) ([]domain.Sandbox, error) {
	return nil, errors.New("unexpected ListReconcileCandidates")
}

// CreateIdempotent 未被 reconciler 测试使用。
func (s *reconcileStore) CreateIdempotent(context.Context, store.IdempotentCreateRequest) (store.IdempotentCreateResult, error) {
	return store.IdempotentCreateResult{}, errors.New("unexpected CreateIdempotent")
}

// CreateNonIdempotent 未被 reconciler 测试使用。
func (s *reconcileStore) CreateNonIdempotent(context.Context, store.NonIdempotentCreateRequest) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected CreateNonIdempotent")
}

// Renew 未被当前 reconciler 测试使用。
func (s *reconcileStore) Renew(context.Context, store.RenewUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected Renew")
}

// ExpireIntent 未被当前 reconciler 测试使用。
func (s *reconcileStore) ExpireIntent(_ context.Context, update store.ExpireIntentUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireCalls = append(s.expireCalls, update)
	if s.expireErr != nil {
		return domain.Sandbox{}, s.expireErr
	}
	if update.ID != s.record.ID || update.ExpectedRevision != s.record.Revision ||
		s.record.ExpiresAt == nil || !s.record.ExpiresAt.Equal(update.ExpectedExpiresAt) {
		return domain.Sandbox{}, domain.ErrConflict
	}
	*s.events = append(*s.events, "store-expire-intent")
	s.record.DesiredState = domain.DesiredTerminated
	s.record.ObservedState = domain.StateStopping
	s.record.Reason = domain.SandboxReasonTTLExpired
	s.record.Message = "Sandbox lease has expired."
	s.record.Revision++
	return s.record, nil
}

// ScheduleRetry 模拟失败状态、attempt 和绝对 next time 的同一 CAS 更新。
func (s *reconcileStore) ScheduleRetry(_ context.Context, update store.RetryUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.ID != s.record.ID || update.ExpectedRevision != s.record.Revision {
		return domain.Sandbox{}, domain.ErrConflict
	}
	if update.Reason == s.failReason && s.failErr != nil {
		return domain.Sandbox{}, s.failErr
	}
	s.retryCalls = append(s.retryCalls, update)
	*s.events = append(*s.events, "store-update-Failed-"+update.Reason)
	s.record.ObservedState = domain.StateFailed
	s.record.Reason, s.record.Message = update.Reason, update.Message
	s.record.RuntimeID = update.RuntimeID
	s.record.RetryAttempt++
	s.record.NextReconcileAt = &update.NextReconcileAt
	s.record.LastReconcileAt = &update.AttemptedAt
	s.record.Revision++
	return s.record, nil
}

// ResetRetry 模拟最终状态和 retry metadata 的同一 CAS 写入。
func (s *reconcileStore) ResetRetry(_ context.Context, update store.RetryResetUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	observed := update.Observed
	if observed.ID != s.record.ID || observed.ExpectedRevision != s.record.Revision {
		return domain.Sandbox{}, domain.ErrConflict
	}
	if observed.Reason == s.failReason && s.failErr != nil {
		return domain.Sandbox{}, s.failErr
	}
	s.resetCalls = append(s.resetCalls, update)
	s.updates = append(s.updates, observed)
	*s.events = append(*s.events, "store-update-"+string(observed.State)+"-"+observed.Reason)
	s.record.ObservedState, s.record.Reason, s.record.Message = observed.State, observed.Reason, observed.Message
	s.record.RuntimeID = observed.RuntimeID
	s.record.RetryAttempt, s.record.NextReconcileAt = 0, nil
	reconciledAt := update.ReconciledAt
	s.record.LastReconcileAt = &reconciledAt
	s.record.Revision++
	return s.record, nil
}

// RecordHealthResult 未被当前 reconciler 测试使用。
func (s *reconcileStore) RecordHealthResult(context.Context, store.HealthResultUpdate) (domain.Sandbox, error) {
	return domain.Sandbox{}, errors.New("unexpected RecordHealthResult")
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

// CheckSandboxEgress 记录只读 outbound readiness gate。
func (r *recordingRuntime) CheckSandboxEgress(_ context.Context, _ string, identity string) error {
	*r.events = append(*r.events, "runtime-egress-gate")
	if identity == "" {
		return errors.New("missing network identity")
	}
	return nil
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
	events          *[]string
	err             error
	protocolVersion int
	networkIdentity string
}

// ProbeNetwork 返回同一 runner health 的受验证 netns identity。
func (p *recordingProbe) ProbeNetwork(_ context.Context, _ string, protocolVersion int) (string, error) {
	*p.events = append(*p.events, "runner-network-probe")
	p.protocolVersion = protocolVersion
	return p.networkIdentity, p.err
}

// recordingShutdown 记录 runner 关闭顺序与错误。
type recordingShutdown struct {
	events *[]string
	err    error
}

// Shutdown 模拟固定 cancel-all 端口。
func (s *recordingShutdown) Shutdown(context.Context, string) error {
	*s.events = append(*s.events, "runner-shutdown")
	return s.err
}

// Probe 记录调用并返回预设错误。
func (p *recordingProbe) Probe(_ context.Context, _ string, protocolVersion int) error {
	*p.events = append(*p.events, "runner-probe")
	p.protocolVersion = protocolVersion
	return p.err
}

// compensatedFailure 模拟 Ensure 补偿失败后返回的 cleanup pending 包装。
type compensatedFailure struct {
	operationErr error
}

// Error 返回固定测试错误文本。
func (*compensatedFailure) Error() string {
	return "compensation failed"
}

// Unwrap 保留原始创建失败。
func (e *compensatedFailure) Unwrap() error {
	return e.operationErr
}

// FailureReason 把外层错误标记为 cleanup pending。
func (*compensatedFailure) FailureReason() string {
	return runtimeport.FailureReasonCleanupPending
}

// OperationError 返回补偿前的原始创建错误。
func (e *compensatedFailure) OperationError() error {
	return e.operationErr
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
