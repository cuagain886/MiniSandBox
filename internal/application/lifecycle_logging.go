package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
)

// LifecycleOperations 是 lifecycle logging decorator 包装的应用用例集合。
type LifecycleOperations interface {
	// CreateAccepted 原子提交创建或精确重放结果。
	CreateAccepted(context.Context, CreateSandbox) (IdempotentCreateOutcome, error)
	// Get 返回持久化 sandbox。
	Get(context.Context, string) (domain.Sandbox, error)
	// Delete 提交终止意图。
	Delete(context.Context, DeleteSandbox) (domain.Sandbox, error)
	// Renew 延长有效租约。
	Renew(context.Context, RenewSandbox) (domain.Sandbox, error)
}

// LoggingSandboxService 为 lifecycle 用例添加固定且不含用户内容的 start/result 日志。
type LoggingSandboxService struct {
	next   LifecycleOperations
	logger *logging.Logger
	clock  Clock
}

// NewLoggingSandboxService 创建 lifecycle 日志装饰器。
func NewLoggingSandboxService(next LifecycleOperations, logger *logging.Logger, clock Clock) (*LoggingSandboxService, error) {
	if next == nil || logger == nil || clock == nil {
		return nil, fmt.Errorf("lifecycle logging dependencies: %w", domain.ErrInvalid)
	}
	return &LoggingSandboxService{next: next, logger: logger, clock: clock}, nil
}

// CreateAccepted 记录 create 安全摘要、幂等分支与最终结果。
func (s *LoggingSandboxService) CreateAccepted(ctx context.Context, command CreateSandbox) (IdempotentCreateOutcome, error) {
	started := s.clock.Now()
	operation := mustLogValue("sandbox.create")
	s.logger.Log(ctx, slog.LevelInfo, mustLogValue("operation.start"), mustValueAttr(logging.FieldOperation, operation),
		mustValueAttr(logging.FieldImageHash, mustLogValue(hashImageReference(command.Image))),
		mustUintAttr(logging.FieldFieldCount, createFieldCount(command)),
		mustBoolAttr(logging.FieldIdempotencyPresent, command.Idempotency != nil))
	outcome, err := s.next.CreateAccepted(ctx, command)
	attrs := []logging.Attr{mustValueAttr(logging.FieldOperation, operation),
		mustDurationAttr(logging.FieldDurationMS, elapsed(s.clock.Now(), started)),
		mustValueAttr(logging.FieldResult, mustLogValue(logResult(err))),
		mustValueAttr(logging.FieldIdempotencyOutcome, mustLogValue(idempotencyOutcome(command.Idempotency != nil, outcome, err)))}
	attrs = appendSandboxLogID(attrs, outcome.SandboxID)
	if err != nil {
		attrs = append(attrs, mustValueAttr(logging.FieldErrorCode, mustLogValue(lifecycleErrorCode(err))))
	}
	s.logger.Log(ctx, logLevel(err), mustLogValue("operation.result"), attrs...)
	return outcome, err
}

// Get 记录读取操作，不记录返回 message、image 或其他领域内容。
func (s *LoggingSandboxService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	started := s.logStart(ctx, "sandbox.get", id)
	result, err := s.next.Get(ctx, id)
	s.logResult(ctx, "sandbox.get", id, started, err)
	return result, err
}

// Delete 记录终止意图提交结果，不记录 Store 或 runtime 错误文本。
func (s *LoggingSandboxService) Delete(ctx context.Context, command DeleteSandbox) (domain.Sandbox, error) {
	started := s.logStart(ctx, "sandbox.delete", command.SandboxID)
	result, err := s.next.Delete(ctx, command)
	s.logResult(ctx, "sandbox.delete", command.SandboxID, started, err)
	return result, err
}

// Renew 记录续租结果，不记录客户端提交或当前 expiry 的具体值。
func (s *LoggingSandboxService) Renew(ctx context.Context, command RenewSandbox) (domain.Sandbox, error) {
	started := s.logStart(ctx, "sandbox.renew", command.SandboxID)
	result, err := s.next.Renew(ctx, command)
	s.logResult(ctx, "sandbox.renew", command.SandboxID, started, err)
	return result, err
}

func (s *LoggingSandboxService) logStart(ctx context.Context, operation, sandboxID string) time.Time {
	started := s.clock.Now()
	attrs := []logging.Attr{mustValueAttr(logging.FieldOperation, mustLogValue(operation))}
	attrs = appendSandboxLogID(attrs, sandboxID)
	s.logger.Log(ctx, slog.LevelInfo, mustLogValue("operation.start"), attrs...)
	return started
}

func (s *LoggingSandboxService) logResult(ctx context.Context, operation, sandboxID string, started time.Time, err error) {
	attrs := []logging.Attr{
		mustValueAttr(logging.FieldOperation, mustLogValue(operation)),
		mustDurationAttr(logging.FieldDurationMS, elapsed(s.clock.Now(), started)),
		mustValueAttr(logging.FieldResult, mustLogValue(logResult(err))),
	}
	attrs = appendSandboxLogID(attrs, sandboxID)
	if err != nil {
		attrs = append(attrs, mustValueAttr(logging.FieldErrorCode, mustLogValue(lifecycleErrorCode(err))))
	}
	s.logger.Log(ctx, logLevel(err), mustLogValue("operation.result"), attrs...)
}

func appendSandboxLogID(attrs []logging.Attr, value string) []logging.Attr {
	id, err := logging.NewSafeID(logging.IDKindSandbox, value)
	if err != nil {
		return attrs
	}
	attr, _ := logging.IDAttr(logging.FieldSandboxID, id)
	return append(attrs, attr)
}

func hashImageReference(image string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("minisandbox/log/image/v1\x00"+image)))
}

func createFieldCount(command CreateSandbox) uint64 {
	count := uint64(1)
	if command.Outbound {
		count++
	}
	if command.TTLSeconds != nil {
		count++
	}
	if command.Idempotency != nil {
		count++
	}
	return count
}

func idempotencyOutcome(present bool, outcome IdempotentCreateOutcome, err error) string {
	if !present {
		return "absent"
	}
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return "conflict"
	}
	if err != nil {
		return "failed"
	}
	if outcome.Replayed {
		return "replay"
	}
	return "accepted"
}

func lifecycleErrorCode(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, domain.ErrConflict):
		return "CONFLICT"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, domain.ErrSandboxLimitReached):
		return "SANDBOX_LIMIT_REACHED"
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrInvalidTTL), errors.Is(err, domain.ErrInvalidExpiration):
		return "INVALID_REQUEST"
	default:
		return "INTERNAL_ERROR"
	}
}

func logResult(err error) string {
	if err == nil {
		return "success"
	}
	return "failure"
}

func logLevel(err error) slog.Level {
	if err == nil {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}

func elapsed(ended, started time.Time) time.Duration {
	if ended.Before(started) {
		return 0
	}
	return ended.Sub(started)
}

func mustLogValue(value string) logging.SafeValue {
	result, err := logging.NewSafeValue(value)
	if err != nil {
		panic(err)
	}
	return result
}

func mustValueAttr(field logging.Field, value logging.SafeValue) logging.Attr {
	result, err := logging.ValueAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}

func mustUintAttr(field logging.Field, value uint64) logging.Attr {
	result, err := logging.UintAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}

func mustDurationAttr(field logging.Field, value time.Duration) logging.Attr {
	result, err := logging.DurationAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}

func mustBoolAttr(field logging.Field, value bool) logging.Attr {
	result, err := logging.BoolAttr(field, value)
	if err != nil {
		panic(err)
	}
	return result
}
