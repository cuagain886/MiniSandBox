package reconcile

import (
	"context"
	"testing"
	"time"

	storeport "minisandbox/internal/store"
)

type anomalyFinalizerRepository struct {
	resolution *storeport.RuntimeAnomalyResolution
}

func (*anomalyFinalizerRepository) ObserveRuntimeAnomaly(context.Context, storeport.RuntimeAnomalyObservation) (storeport.RuntimeAnomaly, error) {
	return storeport.RuntimeAnomaly{}, nil
}

func (*anomalyFinalizerRepository) ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error) {
	return nil, nil
}

func (r *anomalyFinalizerRepository) ResolveRuntimeAnomaliesNotObserved(_ context.Context, resolution storeport.RuntimeAnomalyResolution) (int, error) {
	copy := resolution
	copy.ActiveResourceKeys = append([]string(nil), resolution.ActiveResourceKeys...)
	r.resolution = &copy
	return 2, nil
}

// TestFinalizeAnomalyScanRequiresCompleteCoverage 验证 container、volume 或 filesystem 任一部分失败都不 resolve。
func TestFinalizeAnomalyScanRequiresCompleteCoverage(t *testing.T) {
	started := time.Now().UTC()
	complete := InventoryCoverage{ContainersComplete: true, VolumesComplete: true, FilesystemComplete: true}
	for _, missing := range []string{"containers", "volumes", "filesystem"} {
		coverage := complete
		switch missing {
		case "containers":
			coverage.ContainersComplete = false
		case "volumes":
			coverage.VolumesComplete = false
		case "filesystem":
			coverage.FilesystemComplete = false
		}
		repository := &anomalyFinalizerRepository{}
		resolved, err := FinalizeAnomalyScan(context.Background(), repository, ActualResourceInventory{}, coverage, started, started.Add(time.Second))
		if err != nil || resolved != 0 || repository.resolution != nil {
			t.Fatalf("partial %s resolved: count=%d request=%#v err=%v", missing, resolved, repository.resolution, err)
		}
	}
}

// TestFinalizeAnomalyScanPassesCurrentGenerationKeys 验证完整扫描使用安全事实键保护仍存在的异常。
func TestFinalizeAnomalyScanPassesCurrentGenerationKeys(t *testing.T) {
	started := time.Now().UTC()
	inventory := ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{{
		SandboxID: "still-present", Anomalies: []ActualAnomaly{{Code: ActualAnomalyDuplicateMain, Resource: "main"}},
	}}}
	repository := &anomalyFinalizerRepository{}
	resolved, err := FinalizeAnomalyScan(context.Background(), repository, inventory,
		InventoryCoverage{ContainersComplete: true, VolumesComplete: true, FilesystemComplete: true}, started, started.Add(time.Second))
	if err != nil || resolved != 2 || repository.resolution == nil || len(repository.resolution.ActiveResourceKeys) != 1 ||
		repository.resolution.ScanStartedAt != started {
		t.Fatalf("complete finalization: count=%d request=%#v err=%v", resolved, repository.resolution, err)
	}
}
