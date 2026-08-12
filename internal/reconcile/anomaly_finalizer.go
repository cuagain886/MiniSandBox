package reconcile

import (
	"context"
	"fmt"
	"sort"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// InventoryCoverage 表示一次恢复扫描是否完整覆盖全部异常资源来源。
type InventoryCoverage struct {
	// ContainersComplete 表示 Docker container inventory 全量成功。
	ContainersComplete bool
	// VolumesComplete 表示 Docker volume inventory 全量成功。
	VolumesComplete bool
	// FilesystemComplete 表示受管 runtime directory inventory 全量成功。
	FilesystemComplete bool
}

// Complete 仅在三个事实源全部成功时返回 true；部分扫描绝不能据此解决异常。
func (c InventoryCoverage) Complete() bool {
	return c.ContainersComplete && c.VolumesComplete && c.FilesystemComplete
}

// FinalizeAnomalyScan 在完整 inventory 后解决本轮未见异常；partial/global error 路径必须传不完整 coverage。
func FinalizeAnomalyScan(ctx context.Context, repository storeport.RuntimeAnomalyRepository, inventory ActualResourceInventory,
	coverage InventoryCoverage, scanStartedAt, completedAt time.Time) (int, error) {
	if repository == nil || scanStartedAt.IsZero() || completedAt.IsZero() || completedAt.Before(scanStartedAt) {
		return 0, fmt.Errorf("finalize anomaly scan: %w", domain.ErrInvalid)
	}
	if !coverage.Complete() {
		return 0, nil
	}
	keys := activeRuntimeAnomalyKeys(inventory, scanStartedAt)
	return repository.ResolveRuntimeAnomaliesNotObserved(ctx, storeport.RuntimeAnomalyResolution{
		ActiveResourceKeys: keys, ScanStartedAt: scanStartedAt.UTC(), ResolvedAt: completedAt.UTC(),
	})
}

func activeRuntimeAnomalyKeys(inventory ActualResourceInventory, observedAt time.Time) []string {
	unique := make(map[string]struct{})
	for _, snapshot := range inventory.Sandboxes {
		for _, anomaly := range snapshot.Anomalies {
			unique[runtimeAnomalyObservation(snapshot.SandboxID, anomaly, observedAt).ResourceKey] = struct{}{}
		}
	}
	for _, anomaly := range inventory.UnscopedAnomalies {
		unique[runtimeAnomalyObservation("", anomaly, observedAt).ResourceKey] = struct{}{}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
