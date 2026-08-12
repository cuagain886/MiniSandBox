package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	sqlitestore "minisandbox/internal/store/sqlite"
)

type startupRecoveryRuntime struct {
	events    *[]string
	failStage string
	block     bool
}

func (r *startupRecoveryRuntime) EnsureRecoveryEgressNetwork(context.Context) error {
	*r.events = append(*r.events, "network")
	return r.failure("network")
}

func (r *startupRecoveryRuntime) InventoryManagedContainers(ctx context.Context) ([]runtimeport.ManagedContainerObservation, error) {
	*r.events = append(*r.events, "containers")
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, r.failure("containers")
}

func (r *startupRecoveryRuntime) InventoryManagedVolumes(context.Context) ([]runtimeport.ManagedVolumeObservation, error) {
	*r.events = append(*r.events, "volumes")
	return nil, r.failure("volumes")
}

func (r *startupRecoveryRuntime) InventoryRuntimeDirectories(context.Context) ([]runtimeport.RuntimeDirectoryObservation, error) {
	*r.events = append(*r.events, "directories")
	return nil, r.failure("directories")
}

func (r *startupRecoveryRuntime) failure(stage string) error {
	if r.failStage == stage {
		return errors.New("injected " + stage)
	}
	return nil
}

// TestStartupRecoveryCoordinatorRunsGateOrderAndIsRepeatable 验证完整顺序且重复 bootstrap 保持幂等调用形态。
func TestStartupRecoveryCoordinatorRunsGateOrderAndIsRepeatable(t *testing.T) {
	events := make([]string, 0)
	runtime := &startupRecoveryRuntime{events: &events}
	coordinator := newStartupCoordinatorForTest(t, runtime, &events, "")
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantOnce := []string{"network", "containers", "volumes", "directories", "recover", "ttl", "queue"}
	want := append(append([]string(nil), wantOnce...), wantOnce...)
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v want %v", events, want)
	}
}

// TestStartupRecoveryCoordinatorStopsAtEveryGlobalFailure 验证每个启动全局故障都阻止后续阶段。
func TestStartupRecoveryCoordinatorStopsAtEveryGlobalFailure(t *testing.T) {
	stages := []string{"network", "containers", "volumes", "directories", "recover", "ttl", "queue"}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			events := make([]string, 0)
			runtime := &startupRecoveryRuntime{events: &events, failStage: stage}
			coordinator := newStartupCoordinatorForTest(t, runtime, &events, stage)
			if err := coordinator.Run(context.Background()); err == nil {
				t.Fatal("expected failure")
			}
			if len(events) != index+1 || events[len(events)-1] != stage {
				t.Fatalf("later stage ran after %s: %v", stage, events)
			}
		})
	}
}

// TestStartupRecoveryCoordinatorTotalTimeoutAndShutdownCancellation 验证总超时及父级 shutdown 都不会继续到恢复阶段。
func TestStartupRecoveryCoordinatorTotalTimeoutAndShutdownCancellation(t *testing.T) {
	events := make([]string, 0)
	runtime := &startupRecoveryRuntime{events: &events, block: true}
	coordinator, err := NewStartupRecoveryCoordinator(runtime, runtime, StartupRecoveryStages{
		Recover: func(context.Context, ActualResourceInventory, time.Time) error {
			events = append(events, "recover")
			return nil
		},
		RecoverTTL: func(context.Context) error { return nil }, QueueDue: func(context.Context) error { return nil },
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Run(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if hasEvent(events, "recover") {
		t.Fatalf("recovery ran after timeout: %v", events)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	runtime.block = false
	if err := coordinator.Run(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown cancellation: %v", err)
	}
}

// TestInventoryRecoveryExecutorImportsTrustedOrphan 验证完整可信 orphan 在启动组合路径中落库并入队。
func TestInventoryRecoveryExecutorImportsTrustedOrphan(t *testing.T) {
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now, actual, _, expected := orphanImportFixture(t)
	woken := make([]string, 0)
	executor, err := NewInventoryRecoveryExecutor(database, database, newManualClock(now), func(id string) bool {
		woken = append(woken, id)
		return true
	}, true, expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Recover(context.Background(), ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{actual}}, now); err != nil {
		t.Fatal(err)
	}
	record, err := database.Get(context.Background(), actual.SandboxID)
	if err != nil || record.Origin != domain.SandboxOriginRecoveredOrphan || len(woken) != 1 || woken[0] != actual.SandboxID {
		t.Fatalf("trusted import: record=%#v wake=%v err=%v", record, woken, err)
	}
}

func newStartupCoordinatorForTest(t *testing.T, runtime *startupRecoveryRuntime, events *[]string, failStage string) *StartupRecoveryCoordinator {
	t.Helper()
	stage := func(name string) error {
		*events = append(*events, name)
		if failStage == name {
			return errors.New("injected " + name)
		}
		return nil
	}
	coordinator, err := NewStartupRecoveryCoordinator(runtime, runtime, StartupRecoveryStages{
		Recover:    func(context.Context, ActualResourceInventory, time.Time) error { return stage("recover") },
		RecoverTTL: func(context.Context) error { return stage("ttl") },
		QueueDue:   func(context.Context) error { return stage("queue") },
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

var _ runtimeport.RecoveryInventory = (*startupRecoveryRuntime)(nil)
var _ runtimeport.EgressRecoveryBootstrap = (*startupRecoveryRuntime)(nil)
