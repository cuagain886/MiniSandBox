package reconcile

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

const ttlExpireCASAttempts = 3

// TTLExpirationStore 是到期 coordinator 使用的读取与单一 CAS 写入端口。
type TTLExpirationStore interface {
	TTLDueStore
	// ExpireIntent 仅在 revision、expiry 和到期时间仍匹配时提交终止意图。
	ExpireIntent(context.Context, storeport.ExpireIntentUpdate) (domain.Sandbox, error)
}

// TTLWake 是成功提交终止意图后的尽力 reconcile 唤醒。
type TTLWake func(string) bool

// TTLExpirationCoordinator 复核 timer entry 并仅通过 Store CAS 提交删除意图。
type TTLExpirationCoordinator struct {
	store     TTLExpirationStore
	validator *TTLDueValidator
	index     TTLIndex
	wake      TTLWake
	metrics   TTLExpirationMetrics
}

// TTLExpirationMetrics 只在 expiry intent 首次成功提交后递增。
type TTLExpirationMetrics interface{ ObserveLeaseExpired() }

// SetMetrics 为到期协调器装配低基数计数端口。
func (c *TTLExpirationCoordinator) SetMetrics(metrics TTLExpirationMetrics) { c.metrics = metrics }

// NewTTLExpirationCoordinator 创建不直接访问 Runtime 的 TTL 到期协调器。
func NewTTLExpirationCoordinator(store TTLExpirationStore, index TTLIndex, clock Clock, wake TTLWake) *TTLExpirationCoordinator {
	return &TTLExpirationCoordinator{
		store: store, validator: NewTTLDueValidator(store, index, clock), index: index, wake: wake,
	}
}

// ExpireEntry 复核当前租约，以有限 CAS 重读提交 DesiredTerminated 并尽力 Wake。
func (c *TTLExpirationCoordinator) ExpireEntry(ctx context.Context, entry TTLHeapEntry) error {
	if c == nil || c.store == nil || c.validator == nil || c.index == nil {
		return fmt.Errorf("expire TTL entry: %w", domain.ErrInvalid)
	}
	for attempt := 0; attempt < ttlExpireCASAttempts; attempt++ {
		validated, ok, err := c.validator.Validate(ctx, entry)
		if err != nil || !ok {
			return err
		}
		updated, err := c.store.ExpireIntent(ctx, storeport.ExpireIntentUpdate{
			ID: validated.Sandbox.ID, ExpectedRevision: validated.Sandbox.Revision,
			ExpectedExpiresAt: entry.ExpectedExpiresAt.UTC(), Now: validated.CheckedAt,
		})
		if err == nil {
			if c.metrics != nil {
				c.metrics.ObserveLeaseExpired()
			}
			c.index.Remove(updated.ID)
			if c.wake != nil {
				_ = c.wake(updated.ID)
			}
			return nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			if errors.Is(err, domain.ErrNotFound) {
				c.index.Remove(entry.SandboxID)
				return nil
			}
			return fmt.Errorf("expire TTL entry: commit intent: %w", err)
		}
		// CAS 冲突可能只是 observed/retry revision 变化；重新读取并让 validator
		// 再次确认 expiry。renew/delete 已获胜时下一轮会安全失效。
	}
	return fmt.Errorf("expire TTL entry: CAS retry exhausted: %w", domain.ErrConflict)
}

var _ TTLExpirationStore = (storeport.Store)(nil)
