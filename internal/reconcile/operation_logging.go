package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
)

// OperationLogger 记录 reconcile、retry 与 startup recovery 的稳定安全事件。
// 它不接收原始错误、inspect snapshot 或资源标签，因而不能改变收敛决策或恢复动作。
type OperationLogger struct {
	logger *logging.Logger
	clock  Clock
}

// NewOperationLogger 创建与业务时钟分离的 reconcile 日志记录器。
func NewOperationLogger(logger *logging.Logger, clock Clock) (*OperationLogger, error) {
	if logger == nil || clock == nil {
		return nil, fmt.Errorf("reconcile operation logging dependencies: %w", domain.ErrInvalid)
	}
	return &OperationLogger{logger: logger, clock: clock}, nil
}

func (l *OperationLogger) reconcileStart(ctx context.Context, sandboxID string) time.Time {
	started := l.clock.Now()
	l.logger.Log(ctx, slog.LevelInfo, reconcileSafeValue("operation.start"),
		reconcileValueAttr(logging.FieldOperation, "sandbox.reconcile"), reconcileSandboxAttr(sandboxID))
	return started
}

func (l *OperationLogger) reconcileResult(ctx context.Context, sandboxID string, started time.Time, err error) {
	attrs := []logging.Attr{
		reconcileValueAttr(logging.FieldOperation, "sandbox.reconcile"),
		reconcileDurationAttr(logging.FieldDurationMS, reconcileElapsed(l.clock.Now(), started)),
		reconcileValueAttr(logging.FieldResult, reconcileResultValue(err)),
		reconcileSandboxAttr(sandboxID),
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, reconcileValueAttr(logging.FieldErrorCode, reconcileErrorCode(err)))
	}
	l.logger.Log(ctx, level, reconcileSafeValue("operation.result"), attrs...)
}

func (l *OperationLogger) retryDecision(ctx context.Context, sandboxID string, operation RetryOperation,
	attempt uint32, delay time.Duration, class RetryErrorClass, result string) {
	attrs := []logging.Attr{
		reconcileValueAttr(logging.FieldOperation, "retry."+string(operation)),
		reconcileUintAttr(logging.FieldAttempt, uint64(attempt)),
		reconcileDurationAttr(logging.FieldDelayMS, delay),
		reconcileValueAttr(logging.FieldErrorClass, string(class)),
		reconcileValueAttr(logging.FieldErrorCode, "RETRY_"+strings.ToUpper(string(class))),
		reconcileValueAttr(logging.FieldResult, result),
		reconcileSandboxAttr(sandboxID),
	}
	l.logger.Log(ctx, slog.LevelWarn, reconcileSafeValue("retry.decision"), attrs...)
}

func (l *OperationLogger) recoveryPlan(ctx context.Context, plan RecoveryPlan, actual *ActualResourceSnapshot) {
	attrs := []logging.Attr{
		reconcileValueAttr(logging.FieldOperation, "recovery.plan"),
		reconcileValueAttr(logging.FieldResult, strings.ToLower(string(plan.Action))),
		reconcileSandboxAttr(plan.SandboxID),
	}
	anomalyAttrs := anomalyLogAttrs(actual)
	if len(anomalyAttrs) == 0 {
		attrs = append(attrs, reconcileValueAttr(logging.FieldClassification, plan.Reason))
	} else {
		attrs = append(attrs, anomalyAttrs...)
	}
	l.logger.Log(ctx, slog.LevelInfo, reconcileSafeValue("recovery.plan"), attrs...)
}

func (l *OperationLogger) recoveryResult(ctx context.Context, plan RecoveryPlan, started time.Time, err error) {
	attrs := []logging.Attr{
		reconcileValueAttr(logging.FieldOperation, "recovery."+strings.ToLower(string(plan.Action))),
		reconcileDurationAttr(logging.FieldDurationMS, reconcileElapsed(l.clock.Now(), started)),
		reconcileValueAttr(logging.FieldResult, reconcileResultValue(err)),
		reconcileSandboxAttr(plan.SandboxID),
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, reconcileValueAttr(logging.FieldErrorCode, reconcileErrorCode(err)))
	}
	l.logger.Log(ctx, level, reconcileSafeValue("recovery.result"), attrs...)
}

func anomalyLogAttrs(actual *ActualResourceSnapshot) []logging.Attr {
	if actual == nil || len(actual.Anomalies) == 0 {
		return nil
	}
	observation := runtimeAnomalyObservation(actual.SandboxID, actual.Anomalies[0], time.Unix(0, 0).UTC())
	prefix := observation.SafeFingerprint
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return []logging.Attr{
		reconcileValueAttr(logging.FieldClassification, string(observation.Classification)),
		reconcileValueAttr(logging.FieldFingerprintPrefix, prefix),
	}
}

func reconcileResultValue(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrLeaseConflict) {
		return "cas_conflict"
	}
	return "failure"
}

func reconcileErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	case errors.Is(err, context.DeadlineExceeded):
		return "DEADLINE_EXCEEDED"
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrLeaseConflict):
		return "CAS_CONFLICT"
	case errors.Is(err, domain.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, domain.ErrInvalid):
		return "INVALID"
	default:
		return "INTERNAL_ERROR"
	}
}

func reconcileElapsed(ended, started time.Time) time.Duration {
	if ended.Before(started) {
		return 0
	}
	return ended.Sub(started)
}

func reconcileSafeValue(value string) logging.SafeValue {
	result, err := logging.NewSafeValue(value)
	if err != nil {
		panic(err)
	}
	return result
}

func reconcileValueAttr(field logging.Field, value string) logging.Attr {
	result, err := logging.ValueAttr(field, reconcileSafeValue(value))
	if err != nil {
		panic(err)
	}
	return result
}

func reconcileUintAttr(field logging.Field, value uint64) logging.Attr {
	result, err := logging.UintAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}

func reconcileDurationAttr(field logging.Field, value time.Duration) logging.Attr {
	result, err := logging.DurationAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}

func reconcileSandboxAttr(value string) logging.Attr {
	id, err := logging.NewSafeID(logging.IDKindSandbox, value)
	if err != nil {
		return logging.Attr{}
	}
	result, _ := logging.IDAttr(logging.FieldSandboxID, id)
	return result
}
