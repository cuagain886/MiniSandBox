package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	runtimeport "minisandbox/internal/runtime"
)

// TestDependencyHealthFreshnessAndRecovery 验证短暂失败不降级，超过 freshness
// 后 Store/Docker 分别降级，Docker gate 随成功探测自动恢复。
func TestDependencyHealthFreshnessAndRecovery(t *testing.T) {
	clock := newManualClock(time.Date(2027, 6, 1, 2, 3, 4, 0, time.UTC))
	storeProbe := &healthProbe{}
	dockerProbe := &healthProbe{}
	readiness := &healthReadiness{store: true, docker: true}
	gate := runtimeport.NewAvailabilityGate(true)
	monitor, err := NewDependencyHealthMonitor(storeProbe, dockerProbe, readiness, gate, clock, time.Second, time.Second, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("new monitor: %v", err)
	}
	failure := errors.New("dependency failed")
	storeProbe.setError(failure)
	dockerProbe.setError(failure)
	clock.Advance(5 * time.Second)
	monitor.ProbeOnce(context.Background())
	if storeReady, dockerReady := readiness.snapshot(); !storeReady || !dockerReady {
		t.Fatalf("transient failure degraded readiness: store=%v docker=%v", storeReady, dockerReady)
	}
	if err := gate.WaitAvailable(context.Background()); err != nil {
		t.Fatalf("transient failure closed gate: %v", err)
	}
	clock.Advance(5 * time.Second)
	monitor.ProbeOnce(context.Background())
	if storeReady, dockerReady := readiness.snapshot(); storeReady || dockerReady {
		t.Fatalf("stale dependencies stayed ready: store=%v docker=%v", storeReady, dockerReady)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.WaitAvailable(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale Docker gate: %v", err)
	}
	storeProbe.setError(nil)
	dockerProbe.setError(nil)
	monitor.ProbeOnce(context.Background())
	if storeReady, dockerReady := readiness.snapshot(); !storeReady || !dockerReady {
		t.Fatalf("healthy dependencies did not recover: store=%v docker=%v", storeReady, dockerReady)
	}
	if err := gate.WaitAvailable(context.Background()); err != nil {
		t.Fatalf("recovered Docker gate: %v", err)
	}
}

// TestDependencyHealthUsesIndependentTimeouts 验证阻塞 Store probe 不妨碍 Docker
// 成功结果落地，且快照只保存安全 timeout 类别。
func TestDependencyHealthUsesIndependentTimeouts(t *testing.T) {
	clock := newManualClock(time.Now().UTC())
	storeProbe := &healthProbe{waitForContext: true}
	dockerProbe := &healthProbe{}
	readiness := &healthReadiness{store: true, docker: true}
	monitor, err := NewDependencyHealthMonitor(storeProbe, dockerProbe, readiness, runtimeport.NewAvailabilityGate(true), clock, time.Second, 20*time.Millisecond, time.Second, nil)
	if err != nil {
		t.Fatalf("new monitor: %v", err)
	}
	monitor.ProbeOnce(context.Background())
	snapshot := monitor.Snapshot()
	if snapshot.StoreError != DependencyErrorTimeout || snapshot.DockerError != DependencyErrorNone || dockerProbe.calls() != 1 {
		t.Fatalf("independent probe snapshot: %#v docker calls=%d", snapshot, dockerProbe.calls())
	}
}

// TestDependencyHealthRunStopsWithContext 验证周期循环不会遗留 goroutine。
func TestDependencyHealthRunStopsWithContext(t *testing.T) {
	clock := newManualClock(time.Now().UTC())
	monitor, _ := NewDependencyHealthMonitor(&healthProbe{}, &healthProbe{}, &healthReadiness{}, runtimeport.NewAvailabilityGate(true), clock, time.Second, time.Second, 2*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx) }()
	select {
	case <-clock.tickerCreated:
	case <-time.After(time.Second):
		t.Fatal("health ticker was not created")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health monitor did not stop")
	}
}

// TestGlobalRuntimeOutageDoesNotIncreaseSandboxRetry 验证全局 gate 关闭时 worker
// 只等待恢复，不把 Docker outage 复制为每个 sandbox 的失败记录。
func TestGlobalRuntimeOutageDoesNotIncreaseSandboxRetry(t *testing.T) {
	gate := runtimeport.NewAvailabilityGate(false)
	events := make([]string, 0, 2)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	runtime := &limitRecordingRuntime{}
	reconciler := New(sandboxStore, runtime, &recordingProbe{events: &events})
	reconciler.availability = gate
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := reconciler.Reconcile(ctx, "sandbox-id"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("global outage wait: %v", err)
	}
	if sandboxStore.record.RetryAttempt != 0 || len(sandboxStore.retryCalls) != 0 || runtime.callCount() != 0 {
		t.Fatalf("global outage was persisted per sandbox: record=%#v calls=%d", sandboxStore.record, runtime.callCount())
	}
}

type healthProbe struct {
	mu             sync.Mutex
	err            error
	callCount      int
	waitForContext bool
}

func (p *healthProbe) ProbeDependency(ctx context.Context) error {
	p.mu.Lock()
	p.callCount++
	wait, err := p.waitForContext, p.err
	p.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (p *healthProbe) setError(err error) { p.mu.Lock(); p.err = err; p.mu.Unlock() }
func (p *healthProbe) calls() int         { p.mu.Lock(); defer p.mu.Unlock(); return p.callCount }

type healthReadiness struct {
	mu            sync.Mutex
	store, docker bool
}

func (r *healthReadiness) SetStore(value bool)  { r.mu.Lock(); r.store = value; r.mu.Unlock() }
func (r *healthReadiness) SetDocker(value bool) { r.mu.Lock(); r.docker = value; r.mu.Unlock() }
func (r *healthReadiness) snapshot() (bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store, r.docker
}
