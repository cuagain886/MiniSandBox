package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

type recordingLeaseProjector struct {
	manifests []runtimeport.LeaseManifest
	err       error
}

func (p *recordingLeaseProjector) Write(manifest runtimeport.LeaseManifest) error {
	p.manifests = append(p.manifests, manifest)
	return p.err
}

// TestReconcileProjectsCurrentRunningLease 验证锁内最新 Store snapshot 决定 manifest。
func TestReconcileProjectsCurrentRunningLease(t *testing.T) {
	events := []string{}
	expiresAt := time.Date(2028, 10, 11, 12, 13, 14, 0, time.UTC)
	sandbox := pendingSandbox()
	sandbox.ObservedState = domain.StateRunning
	sandbox.Reason = domain.SandboxReasonRunning
	sandbox.SpecHash = "spec-hash"
	sandbox.RuntimeID = "runtime-id"
	sandbox.ExpiresAt = &expiresAt
	store := newReconcileStore(&events, sandbox)
	projector := &recordingLeaseProjector{}
	reconciler := New(store, &recordingRuntime{events: &events}, &recordingProbe{events: &events})
	reconciler.SetLeaseProjector(projector)
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(projector.manifests) != 1 {
		t.Fatalf("manifest calls: %#v", projector.manifests)
	}
	manifest := projector.manifests[0]
	if manifest.SandboxID != sandbox.ID || manifest.SpecHash != sandbox.SpecHash ||
		!manifest.ExpiresAt.Equal(expiresAt) || manifest.ProjectedStoreRevision != sandbox.Revision {
		t.Fatalf("manifest: %#v", manifest)
	}
}

// TestReconcileProjectsAfterCreateReachedRunning 验证初建只在最终 Running CAS 后投影。
func TestReconcileProjectsAfterCreateReachedRunning(t *testing.T) {
	events := []string{}
	expiresAt := time.Now().Add(time.Hour).UTC()
	sandbox := pendingSandbox()
	sandbox.ExpiresAt = &expiresAt
	sandbox.SpecHash = "spec-hash"
	store := newReconcileStore(&events, sandbox)
	projector := &recordingLeaseProjector{}
	runtime := &recordingRuntime{events: &events, ensureResult: runtimeport.ActualSandbox{RuntimeID: "runtime-id", RunnerProtocolVersion: 1}}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	reconciler.SetLeaseProjector(projector)
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	if len(projector.manifests) != 1 || projector.manifests[0].ProjectedStoreRevision != store.record.Revision {
		t.Fatalf("post-create manifest=%#v record=%#v", projector.manifests, store.record)
	}
}

// TestReconcileLeaseProjectionFailureUsesRetryPolicy 验证写失败持久化 backoff 且不删除 runtime。
func TestReconcileLeaseProjectionFailureUsesRetryPolicy(t *testing.T) {
	events := []string{}
	expiresAt := time.Now().Add(time.Hour).UTC()
	sandbox := pendingSandbox()
	sandbox.ObservedState, sandbox.Reason = domain.StateRunning, domain.SandboxReasonRunning
	sandbox.ExpiresAt, sandbox.RuntimeID = &expiresAt, "runtime-id"
	store := newReconcileStore(&events, sandbox)
	projector := &recordingLeaseProjector{err: errors.New("write failed")}
	runtime := &recordingRuntime{events: &events}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	reconciler.SetLeaseProjector(projector)
	err := reconciler.Reconcile(context.Background(), sandbox.ID)
	if err == nil || len(store.retryCalls) != 1 || events[len(events)-1] != "store-update-Failed-INTERNAL_ERROR" {
		t.Fatalf("projection failure: err=%v retries=%#v events=%v", err, store.retryCalls, events)
	}
}

// TestReconcileDoesNotProjectDeletingLease 验证 delete 期间不写 manifest 或回写旧租约。
func TestReconcileDoesNotProjectDeletingLease(t *testing.T) {
	events := []string{}
	sandbox := pendingSandbox()
	sandbox.DesiredState = domain.DesiredTerminated
	projector := &recordingLeaseProjector{}
	reconciler := New(newReconcileStore(&events, sandbox), &recordingRuntime{events: &events}, &recordingProbe{events: &events})
	reconciler.SetLeaseProjector(projector)
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	if len(projector.manifests) != 0 {
		t.Fatalf("delete projected lease: %#v", projector.manifests)
	}
}
