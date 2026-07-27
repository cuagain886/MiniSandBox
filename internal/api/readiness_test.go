package api

import (
	"sync"
	"testing"
)

// TestReadinessDefaultAndCombinations 验证零值拒绝就绪且必须满足全部条件。
func TestReadinessDefaultAndCombinations(t *testing.T) {
	var readiness Readiness
	initial := readiness.Snapshot()
	if initial.Ready() {
		t.Fatal("zero-value readiness must not be ready")
	}
	if initial.Store ||
		initial.Docker ||
		initial.Artifact ||
		initial.Recovery ||
		initial.Worker {
		t.Fatalf("zero-value snapshot: %#v", initial)
	}

	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	if readiness.Snapshot().Ready() {
		t.Fatal("readiness must remain false before worker starts")
	}

	readiness.SetWorker(true)
	ready := readiness.Snapshot()
	if !ready.Ready() {
		t.Fatalf("all-ready snapshot: %#v", ready)
	}
	if initial.Ready() {
		t.Fatal("an earlier snapshot must remain immutable")
	}

	readiness.SetDocker(false)
	if readiness.Snapshot().Ready() {
		t.Fatal("losing one required component must make readiness false")
	}
}

// TestReadinessConcurrentAccess 验证各组件更新和快照读取可安全并发。
func TestReadinessConcurrentAccess(t *testing.T) {
	var readiness Readiness
	const iterations = 1_000
	var waitGroup sync.WaitGroup

	setters := []func(bool){
		readiness.SetStore,
		readiness.SetDocker,
		readiness.SetArtifact,
		readiness.SetRecovery,
		readiness.SetWorker,
	}
	for _, setter := range setters {
		setter := setter
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < iterations; index++ {
				setter(index%2 == 0)
			}
		}()
	}
	for reader := 0; reader < 5; reader++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < iterations; index++ {
				_ = readiness.Snapshot().Ready()
			}
		}()
	}
	waitGroup.Wait()

	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	readiness.SetWorker(true)
	if !readiness.Snapshot().Ready() {
		t.Fatal("readiness must remain usable after concurrent access")
	}
}
