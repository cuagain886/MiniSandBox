package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// ObserveRuntimeAnomaly 只接收受限枚举、规范键与 SHA-256 摘要，并按资源键原子合并观测。
func (s *Store) ObserveRuntimeAnomaly(ctx context.Context, observation storeport.RuntimeAnomalyObservation) (storeport.RuntimeAnomaly, error) {
	if err := validateRuntimeAnomalyObservation(observation); err != nil {
		return storeport.RuntimeAnomaly{}, err
	}
	observedAt := observation.ObservedAt.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO runtime_anomalies (
		resource_key, resource_type, classification, safe_fingerprint,
		first_seen_at, last_seen_at, observation_count, resolved_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, NULL)
	ON CONFLICT(resource_key) DO UPDATE SET
		resource_type = excluded.resource_type,
		classification = excluded.classification,
		safe_fingerprint = excluded.safe_fingerprint,
		first_seen_at = MIN(runtime_anomalies.first_seen_at, excluded.first_seen_at),
		last_seen_at = MAX(runtime_anomalies.last_seen_at, excluded.last_seen_at),
		observation_count = CASE
			WHEN runtime_anomalies.observation_count < 4294967295 THEN runtime_anomalies.observation_count + 1
			ELSE runtime_anomalies.observation_count
		END,
		resolved_at = NULL`, observation.ResourceKey, observation.ResourceType, observation.Classification,
		observation.SafeFingerprint, observedAt, observedAt)
	if err != nil {
		return storeport.RuntimeAnomaly{}, fmt.Errorf("observe runtime anomaly: %w", err)
	}
	return s.runtimeAnomalyByKey(ctx, observation.ResourceKey)
}

// ListActiveRuntimeAnomalies 返回未解决异常的安全快照，不暴露数据库内部列或原始 runtime 内容。
func (s *Store) ListActiveRuntimeAnomalies(ctx context.Context) ([]storeport.RuntimeAnomaly, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT resource_key, resource_type, classification, safe_fingerprint,
		first_seen_at, last_seen_at, observation_count, resolved_at
		FROM runtime_anomalies WHERE resolved_at IS NULL ORDER BY resource_key`)
	if err != nil {
		return nil, fmt.Errorf("list active runtime anomalies: %w", err)
	}
	defer rows.Close()
	result := make([]storeport.RuntimeAnomaly, 0)
	for rows.Next() {
		anomaly, err := scanRuntimeAnomaly(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, anomaly)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runtime anomalies: %w", err)
	}
	return result, nil
}

func (s *Store) runtimeAnomalyByKey(ctx context.Context, resourceKey string) (storeport.RuntimeAnomaly, error) {
	anomaly, err := scanRuntimeAnomaly(s.db.QueryRowContext(ctx, `SELECT resource_key, resource_type, classification,
		safe_fingerprint, first_seen_at, last_seen_at, observation_count, resolved_at
		FROM runtime_anomalies WHERE resource_key = ?`, resourceKey))
	if errors.Is(err, sql.ErrNoRows) {
		return storeport.RuntimeAnomaly{}, domain.ErrNotFound
	}
	return anomaly, err
}

type runtimeAnomalyScanner interface {
	Scan(...any) error
}

func scanRuntimeAnomaly(scanner runtimeAnomalyScanner) (storeport.RuntimeAnomaly, error) {
	var anomaly storeport.RuntimeAnomaly
	var firstSeen, lastSeen string
	var resolved *string
	var count int64
	if err := scanner.Scan(&anomaly.ResourceKey, &anomaly.ResourceType, &anomaly.Classification,
		&anomaly.SafeFingerprint, &firstSeen, &lastSeen, &count, &resolved); err != nil {
		return storeport.RuntimeAnomaly{}, err
	}
	var err error
	if anomaly.FirstSeenAt, err = parseStoredTime("runtime_anomaly_first_seen_at", firstSeen); err != nil {
		return storeport.RuntimeAnomaly{}, errors.Join(storeport.ErrCorrupt, err)
	}
	if anomaly.LastSeenAt, err = parseStoredTime("runtime_anomaly_last_seen_at", lastSeen); err != nil {
		return storeport.RuntimeAnomaly{}, errors.Join(storeport.ErrCorrupt, err)
	}
	if anomaly.ResolvedAt, err = parseOptionalStoredTime("runtime_anomaly_resolved_at", resolved); err != nil {
		return storeport.RuntimeAnomaly{}, errors.Join(storeport.ErrCorrupt, err)
	}
	if count < 1 || count > int64(^uint32(0)) {
		return storeport.RuntimeAnomaly{}, storeport.ErrCorrupt
	}
	anomaly.ObservationCount = uint32(count)
	anomaly.ObservedAt = anomaly.LastSeenAt
	return anomaly, nil
}

func validateRuntimeAnomalyObservation(observation storeport.RuntimeAnomalyObservation) error {
	if len(observation.ResourceKey) < 1 || len(observation.ResourceKey) > 256 {
		return fmt.Errorf("%w: invalid runtime anomaly resource key", domain.ErrInvalid)
	}
	for _, value := range observation.ResourceKey {
		if !(unicode.IsLetter(value) && value <= unicode.MaxASCII || unicode.IsDigit(value) || strings.ContainsRune("._:-", value)) {
			return fmt.Errorf("%w: invalid runtime anomaly resource key", domain.ErrInvalid)
		}
	}
	if !validRuntimeAnomalyResourceType(observation.ResourceType) || !validRuntimeAnomalyClassification(observation.Classification) ||
		len(observation.SafeFingerprint) != 64 || strings.Trim(observation.SafeFingerprint, "0123456789abcdef") != "" ||
		observation.ObservedAt.IsZero() {
		return fmt.Errorf("%w: invalid runtime anomaly observation", domain.ErrInvalid)
	}
	return nil
}

func validRuntimeAnomalyResourceType(value storeport.RuntimeAnomalyResourceType) bool {
	switch value {
	case storeport.RuntimeAnomalySandboxBundle, storeport.RuntimeAnomalyMainContainer,
		storeport.RuntimeAnomalyEgressSidecar, storeport.RuntimeAnomalyWorkspaceVolume,
		storeport.RuntimeAnomalyRuntimeDirectory:
		return true
	default:
		return false
	}
}

func validRuntimeAnomalyClassification(value storeport.RuntimeAnomalyClassification) bool {
	switch value {
	case storeport.RuntimeAnomalyIncompleteBundle, storeport.RuntimeAnomalyUnknownSchema,
		storeport.RuntimeAnomalyIdentityMismatch, storeport.RuntimeAnomalySpecHashMismatch,
		storeport.RuntimeAnomalySecurityProfileMismatch, storeport.RuntimeAnomalyNetworkNamespaceMismatch,
		storeport.RuntimeAnomalyLeaseUntrusted, storeport.RuntimeAnomalyDuplicateResource:
		return true
	default:
		return false
	}
}
