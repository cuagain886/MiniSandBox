package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storeport "minisandbox/internal/store"
)

type anomalyRecordingRepository struct {
	observations []storeport.RuntimeAnomalyObservation
	failKey      string
}

func (r *anomalyRecordingRepository) ObserveRuntimeAnomaly(_ context.Context, observation storeport.RuntimeAnomalyObservation) (storeport.RuntimeAnomaly, error) {
	r.observations = append(r.observations, observation)
	if observation.ResourceKey == r.failKey {
		return storeport.RuntimeAnomaly{}, errors.New("injected")
	}
	return storeport.RuntimeAnomaly{RuntimeAnomalyObservation: observation}, nil
}

func (r *anomalyRecordingRepository) ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error) {
	return nil, nil
}

func (r *anomalyRecordingRepository) ResolveRuntimeAnomaliesNotObserved(context.Context, storeport.RuntimeAnomalyResolution) (int, error) {
	return 0, nil
}

// TestRecordActualAnomaliesClassifiesSafeFacts 验证 inventory 异常映射到固定分类且摘要不回显底层值。
func TestRecordActualAnomaliesClassifiesSafeFacts(t *testing.T) {
	repository := &anomalyRecordingRepository{}
	inventory := ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{{
		SandboxID: "safe-id",
		Anomalies: []ActualAnomaly{
			{Code: ActualAnomalySchemaConflict, Resource: "main"},
			{Code: ActualAnomalySpecHashConflict, Resource: "workspace"},
			{Code: ActualAnomalyPolicyConflict, Resource: "egress", Detail: "POLICY_HASH_INVALID"},
			{Code: ActualAnomalyNetNSConflict},
			{Code: ActualAnomalyIdentityConflict, Resource: "directory"},
			{Code: ActualAnomalyDuplicateMain, Resource: "main"},
		},
	}}}
	if err := RecordActualAnomalies(context.Background(), repository, inventory, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := []storeport.RuntimeAnomalyClassification{
		storeport.RuntimeAnomalyUnknownSchema, storeport.RuntimeAnomalySpecHashMismatch,
		storeport.RuntimeAnomalySecurityProfileMismatch, storeport.RuntimeAnomalyNetworkNamespaceMismatch,
		storeport.RuntimeAnomalyIdentityMismatch, storeport.RuntimeAnomalyDuplicateResource,
	}
	for index, observation := range repository.observations {
		if observation.Classification != want[index] || len(observation.SafeFingerprint) != 64 || strings.Contains(observation.SafeFingerprint, "POLICY") {
			t.Fatalf("observation %d: %#v", index, observation)
		}
	}
}

// TestRecordActualAnomaliesContinuesAfterFailure 验证单项持久化失败不阻断后续可信扫描结果。
func TestRecordActualAnomaliesContinuesAfterFailure(t *testing.T) {
	inventory := ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{
		{SandboxID: "first", Anomalies: []ActualAnomaly{{Code: ActualAnomalyEgressMissing}}},
		{SandboxID: "second", Anomalies: []ActualAnomaly{{Code: ActualAnomalyDuplicateWorkspace, Resource: "workspace"}}},
	}}
	first := runtimeAnomalyObservation("first", inventory.Sandboxes[0].Anomalies[0], time.Now())
	repository := &anomalyRecordingRepository{failKey: first.ResourceKey}
	if err := RecordActualAnomalies(context.Background(), repository, inventory, time.Now()); err == nil {
		t.Fatal("expected aggregate error")
	}
	if len(repository.observations) != 2 {
		t.Fatalf("later anomaly was blocked: %#v", repository.observations)
	}
}
