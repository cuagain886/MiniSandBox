package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	storeport "minisandbox/internal/store"
)

// RecordActualAnomalies 把 inventory 中每个安全异常独立持久化；单项失败不会阻断其他异常记录。
func RecordActualAnomalies(ctx context.Context, repository storeport.RuntimeAnomalyRepository, inventory ActualResourceInventory, observedAt time.Time) error {
	var result error
	for _, snapshot := range inventory.Sandboxes {
		for _, anomaly := range snapshot.Anomalies {
			observation := runtimeAnomalyObservation(snapshot.SandboxID, anomaly, observedAt)
			if _, err := repository.ObserveRuntimeAnomaly(ctx, observation); err != nil {
				result = errors.Join(result, fmt.Errorf("record runtime anomaly: %w", err))
			}
		}
	}
	for _, anomaly := range inventory.UnscopedAnomalies {
		observation := runtimeAnomalyObservation("", anomaly, observedAt)
		if _, err := repository.ObserveRuntimeAnomaly(ctx, observation); err != nil {
			result = errors.Join(result, fmt.Errorf("record unscoped runtime anomaly: %w", err))
		}
	}
	return result
}

func runtimeAnomalyObservation(sandboxID string, anomaly ActualAnomaly, observedAt time.Time) storeport.RuntimeAnomalyObservation {
	classification := runtimeAnomalyClassification(anomaly)
	resourceType := runtimeAnomalyResourceType(anomaly.Resource)
	safeFact := strings.Join([]string{"runtime-anomaly-v1", sandboxID, string(anomaly.Code), anomaly.Resource, anomaly.Detail,
		string(resourceType), string(classification)}, "\x00")
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(safeFact)))
	resourceKey := strings.Join([]string{"sandbox", sandboxID, string(classification), string(resourceType)}, ":")
	if sandboxID == "" {
		resourceKey = "unscoped:" + fingerprint
	}
	return storeport.RuntimeAnomalyObservation{
		ResourceKey: resourceKey, ResourceType: resourceType, Classification: classification,
		SafeFingerprint: fingerprint, ObservedAt: observedAt.UTC(),
	}
}

func runtimeAnomalyResourceType(resource string) storeport.RuntimeAnomalyResourceType {
	switch resource {
	case "main":
		return storeport.RuntimeAnomalyMainContainer
	case "egress":
		return storeport.RuntimeAnomalyEgressSidecar
	case "workspace":
		return storeport.RuntimeAnomalyWorkspaceVolume
	case "directory":
		return storeport.RuntimeAnomalyRuntimeDirectory
	default:
		return storeport.RuntimeAnomalySandboxBundle
	}
}

func runtimeAnomalyClassification(anomaly ActualAnomaly) storeport.RuntimeAnomalyClassification {
	if anomaly.Detail == "SCHEMA_UNSUPPORTED" || anomaly.Detail == "SCHEMA_UNKNOWN" {
		return storeport.RuntimeAnomalyUnknownSchema
	}
	switch anomaly.Code {
	case ActualAnomalyDuplicateMain, ActualAnomalyDuplicateEgress, ActualAnomalyDuplicateWorkspace, ActualAnomalyDuplicateDirectory:
		return storeport.RuntimeAnomalyDuplicateResource
	case ActualAnomalySchemaConflict:
		return storeport.RuntimeAnomalyUnknownSchema
	case ActualAnomalyIdentityConflict:
		return storeport.RuntimeAnomalyIdentityMismatch
	case ActualAnomalySpecHashConflict:
		return storeport.RuntimeAnomalySpecHashMismatch
	case ActualAnomalyNetNSConflict:
		return storeport.RuntimeAnomalyNetworkNamespaceMismatch
	case ActualAnomalyProtocolConflict, ActualAnomalyPolicyConflict, ActualAnomalyNetworkProfile:
		return storeport.RuntimeAnomalySecurityProfileMismatch
	default:
		if anomaly.Detail == "MANIFEST_UNSAFE" || anomaly.Detail == "MANIFEST_INVALID" {
			return storeport.RuntimeAnomalyLeaseUntrusted
		}
		return storeport.RuntimeAnomalyIncompleteBundle
	}
}
