package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
)

// TestOperationLoggerCoversReconcileAndRetryBranches 验证成功、重试、永久失败和 CAS 分支均使用固定字段。
func TestOperationLoggerCoversReconcileAndRetryBranches(t *testing.T) {
	logger, clock, output := newOperationLoggerTest(t)
	started := logger.reconcileStart(context.Background(), "sandbox-1")
	clock.Advance(7 * time.Millisecond)
	logger.reconcileResult(context.Background(), "sandbox-1", started, nil)
	logger.retryDecision(context.Background(), "sandbox-1", RetryOperationCreate, 2, 25*time.Millisecond,
		RetryErrorTransient, "scheduled")
	logger.retryDecision(context.Background(), "sandbox-1", RetryOperationRecover, 3, 0,
		RetryErrorPermanent, "terminal")
	logger.retryDecision(context.Background(), "sandbox-1", RetryOperationDelete, 3, 0,
		RetryErrorConflict, "cas_conflict")

	logs := decodeOperationLogs(t, output.String())
	if len(logs) != 5 || logs[1]["result"] != "success" || logs[1]["duration_ms"] != float64(7) {
		t.Fatalf("reconcile logs: %#v", logs)
	}
	if logs[2]["attempt"] != float64(2) || logs[2]["delay_ms"] != float64(25) || logs[2]["result"] != "scheduled" {
		t.Fatalf("retry log: %#v", logs[2])
	}
	if logs[3]["error_class"] != "permanent" || logs[3]["result"] != "terminal" ||
		logs[4]["error_code"] != "RETRY_CONFLICT" || logs[4]["result"] != "cas_conflict" {
		t.Fatalf("terminal/CAS logs: %#v", logs[3:])
	}
}

// TestOperationLoggerRecordsImportAndSafeAnomalyFingerprint 验证 trusted import 与 anomaly 计划可辨识且不泄露事实文本。
func TestOperationLoggerRecordsImportAndSafeAnomalyFingerprint(t *testing.T) {
	logger, clock, output := newOperationLoggerTest(t)
	importPlan := RecoveryPlan{SandboxID: "sandbox-import", Action: RecoveryActionImport, Reason: recoveryPlanReasonTrustedOrphan}
	logger.recoveryPlan(context.Background(), importPlan, nil)
	started := clock.Now()
	clock.Advance(3 * time.Millisecond)
	logger.recoveryResult(context.Background(), importPlan, started, nil)

	anomaly := &ActualResourceSnapshot{SandboxID: "sandbox-anomaly", Anomalies: []ActualAnomaly{{
		Code: ActualAnomalyResourceDamaged, Resource: "main", Detail: "SECRET_INSPECT_VALUE",
	}}}
	anomalyPlan := RecoveryPlan{SandboxID: anomaly.SandboxID, Action: RecoveryActionRecordAnomaly, Reason: recoveryPlanReasonActualAnomaly}
	logger.recoveryPlan(context.Background(), anomalyPlan, anomaly)
	logger.recoveryResult(context.Background(), anomalyPlan, clock.Now(), errors.New("secret raw inspect error"))

	logs := decodeOperationLogs(t, output.String())
	if len(logs) != 4 || logs[0]["result"] != "import" || logs[0]["classification"] != recoveryPlanReasonTrustedOrphan {
		t.Fatalf("import logs: %#v", logs)
	}
	prefix, ok := logs[2]["fingerprint_prefix"].(string)
	if !ok || len(prefix) != 12 || logs[2]["classification"] != "incomplete_bundle" {
		t.Fatalf("anomaly log: %#v", logs[2])
	}
	if strings.Contains(output.String(), "SECRET_INSPECT_VALUE") || strings.Contains(output.String(), "secret raw inspect error") {
		t.Fatalf("raw recovery fact leaked: %s", output.String())
	}
}

// TestOperationLoggerClassifiesReconcileCAS 验证 reconcile 结果不会把 Store 错误文本写入日志。
func TestOperationLoggerClassifiesReconcileCAS(t *testing.T) {
	logger, clock, output := newOperationLoggerTest(t)
	logger.reconcileResult(context.Background(), "sandbox-cas", clock.Now(), fmt.Errorf("secret row: %w", domain.ErrConflict))
	logs := decodeOperationLogs(t, output.String())
	if len(logs) != 1 || logs[0]["result"] != "cas_conflict" || logs[0]["error_code"] != "CAS_CONFLICT" {
		t.Fatalf("CAS log: %#v", logs)
	}
	if strings.Contains(output.String(), "secret row") {
		t.Fatalf("raw CAS error leaked: %s", output.String())
	}
}

// TestOperationLoggerSupportsConcurrentPlans 验证不同 sandbox 并发恢复日志不会交叉或丢失标识。
func TestOperationLoggerSupportsConcurrentPlans(t *testing.T) {
	logger, _, output := newOperationLoggerTest(t)
	const workers = 32
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			logger.recoveryPlan(context.Background(), RecoveryPlan{
				SandboxID: fmt.Sprintf("sandbox-%d", index), Action: RecoveryActionNoOp, Reason: recoveryPlanReasonStable,
			}, nil)
		}(index)
	}
	wait.Wait()
	logs := decodeOperationLogs(t, output.String())
	if len(logs) != workers {
		t.Fatalf("log count: %d", len(logs))
	}
	seen := make(map[string]struct{}, workers)
	for _, entry := range logs {
		seen[entry["sandbox_id"].(string)] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("unique sandbox IDs: %d", len(seen))
	}
}

func newOperationLoggerTest(t *testing.T) (*OperationLogger, *manualClock, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	safeLogger, err := logging.New(slog.New(slog.NewJSONHandler(output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := newManualClock(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	logger, err := NewOperationLogger(safeLogger, clock)
	if err != nil {
		t.Fatal(err)
	}
	return logger, clock, output
}

func decodeOperationLogs(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	result := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		result = append(result, entry)
	}
	return result
}
