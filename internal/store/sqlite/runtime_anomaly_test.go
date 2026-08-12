package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestRuntimeAnomalyRepositoryCoversFixedClassifications 验证所有固定分类都能持久化且不会保存额外原始内容。
func TestRuntimeAnomalyRepositoryCoversFixedClassifications(t *testing.T) {
	database := migrateTestStore(t)
	classifications := []storeport.RuntimeAnomalyClassification{
		storeport.RuntimeAnomalyIncompleteBundle, storeport.RuntimeAnomalyUnknownSchema,
		storeport.RuntimeAnomalyIdentityMismatch, storeport.RuntimeAnomalySpecHashMismatch,
		storeport.RuntimeAnomalySecurityProfileMismatch, storeport.RuntimeAnomalyNetworkNamespaceMismatch,
		storeport.RuntimeAnomalyLeaseUntrusted, storeport.RuntimeAnomalyDuplicateResource,
	}
	for index, classification := range classifications {
		_, err := database.ObserveRuntimeAnomaly(context.Background(), anomalyObservation(
			"sandbox:test-"+string(rune('a'+index)), classification, time.Date(2026, 8, 12, 1, index, 0, 0, time.UTC)))
		if err != nil {
			t.Fatalf("observe %s: %v", classification, err)
		}
	}
	got, err := database.ListActiveRuntimeAnomalies(context.Background())
	if err != nil || len(got) != len(classifications) {
		t.Fatalf("list classifications: count=%d err=%v", len(got), err)
	}
}

// TestRuntimeAnomalyRepositoryUpsertsObservation 验证重复事实不会增行，摘要变化会更新同一事实并保持首末时间边界。
func TestRuntimeAnomalyRepositoryUpsertsObservation(t *testing.T) {
	database := migrateTestStore(t)
	first := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	observation := anomalyObservation("sandbox:one", storeport.RuntimeAnomalyIncompleteBundle, first)
	if _, err := database.ObserveRuntimeAnomaly(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	observation.SafeFingerprint = "b" + observation.SafeFingerprint[1:]
	observation.ObservedAt = first.Add(time.Minute)
	got, err := database.ObserveRuntimeAnomaly(context.Background(), observation)
	if err != nil || got.ObservationCount != 2 || !got.FirstSeenAt.Equal(first) || !got.LastSeenAt.Equal(observation.ObservedAt) ||
		got.SafeFingerprint != observation.SafeFingerprint {
		t.Fatalf("upsert result: %#v err=%v", got, err)
	}
	active, err := database.ListActiveRuntimeAnomalies(context.Background())
	if err != nil || len(active) != 1 {
		t.Fatalf("duplicate row created: %#v err=%v", active, err)
	}
}

// TestRuntimeAnomalyRepositoryConcurrentObservation 验证并发重复扫描只累加同一资源事实。
func TestRuntimeAnomalyRepositoryConcurrentObservation(t *testing.T) {
	database := migrateTestStore(t)
	observation := anomalyObservation("sandbox:concurrent", storeport.RuntimeAnomalyDuplicateResource, time.Now().UTC())
	const workers = 12
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := database.ObserveRuntimeAnomaly(context.Background(), observation)
			errorsOut <- err
		}()
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	active, err := database.ListActiveRuntimeAnomalies(context.Background())
	if err != nil || len(active) != 1 || active[0].ObservationCount != workers {
		t.Fatalf("concurrent upsert: %#v err=%v", active, err)
	}
}

// TestRuntimeAnomalyRepositorySurvivesReopen 验证异常事实跨 SQLite 关闭重开后仍可读取。
func TestRuntimeAnomalyRepositorySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anomaly.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ObserveRuntimeAnomaly(context.Background(), anomalyObservation(
		"sandbox:reopen", storeport.RuntimeAnomalyLeaseUntrusted, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, err := reopened.ListActiveRuntimeAnomalies(context.Background())
	if err != nil || len(active) != 1 || active[0].Classification != storeport.RuntimeAnomalyLeaseUntrusted {
		t.Fatalf("reopen: %#v err=%v", active, err)
	}
}

// TestRuntimeAnomalyRepositoryRejectsUnsafeFields 验证 raw 名称、未知枚举和非摘要不能越过持久化端口。
func TestRuntimeAnomalyRepositoryRejectsUnsafeFields(t *testing.T) {
	database := migrateTestStore(t)
	cases := []storeport.RuntimeAnomalyObservation{
		anomalyObservation("raw path", storeport.RuntimeAnomalyIncompleteBundle, time.Now()),
		anomalyObservation("sandbox:bad-type", "other", time.Now()),
		anomalyObservation("sandbox:bad-hash", storeport.RuntimeAnomalyIncompleteBundle, time.Now()),
	}
	cases[2].SafeFingerprint = "secret runtime label"
	for _, observation := range cases {
		if _, err := database.ObserveRuntimeAnomaly(context.Background(), observation); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("unsafe observation accepted: %#v err=%v", observation, err)
		}
	}
}

func anomalyObservation(key string, classification storeport.RuntimeAnomalyClassification, observedAt time.Time) storeport.RuntimeAnomalyObservation {
	return storeport.RuntimeAnomalyObservation{
		ResourceKey: key, ResourceType: storeport.RuntimeAnomalySandboxBundle,
		Classification: classification, SafeFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ObservedAt: observedAt,
	}
}
