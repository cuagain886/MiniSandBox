package reconcile

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
)

// TTLRecoveryStore 提供启动恢复所需的 active lease keyset 页面及到期 CAS。
type TTLRecoveryStore interface {
	TTLExpirationStore
	// ListActiveLeases 返回 ID 大于 afterID 的 active records，最多 limit 条。
	ListActiveLeases(context.Context, string, int) ([]domain.Sandbox, error)
}

// TTLRecovery 在 timer loop 启动前从 Store 重建全部未来租约。
type TTLRecovery struct {
	store        TTLRecoveryStore
	scheduler    *TTLScheduler
	expiration   *TTLExpirationCoordinator
	pageSize     int
	maximumPages int
}

// NewTTLRecovery 创建有界 keyset 恢复器。
func NewTTLRecovery(store TTLRecoveryStore, scheduler *TTLScheduler, expiration *TTLExpirationCoordinator, pageSize, maximumPages int) (*TTLRecovery, error) {
	if store == nil || scheduler == nil || expiration == nil || pageSize < 1 || maximumPages < 1 {
		return nil, errors.New("invalid TTL recovery configuration")
	}
	return &TTLRecovery{store: store, scheduler: scheduler, expiration: expiration, pageSize: pageSize, maximumPages: maximumPages}, nil
}

// Recover 完整重建 future heap；恢复时已到期记录立即走同一 expiration coordinator。
func (r *TTLRecovery) Recover(ctx context.Context) error {
	afterID := ""
	for pageNumber := 0; pageNumber < r.maximumPages; pageNumber++ {
		page, err := r.store.ListActiveLeases(ctx, afterID, r.pageSize)
		if err != nil {
			return fmt.Errorf("recover active lease page: %w", err)
		}
		previous := afterID
		for _, sandbox := range page {
			if sandbox.ID == "" || sandbox.ID <= previous || sandbox.ExpiresAt == nil {
				return fmt.Errorf("recover active lease page: %w", domain.ErrInvalid)
			}
			entry := TTLHeapEntry{SandboxID: sandbox.ID, ExpectedExpiresAt: sandbox.ExpiresAt.UTC()}
			if sandboxLeaseDue(sandbox, r.scheduler.clock.Now().UTC()) {
				if err := r.expiration.ExpireEntry(ctx, entry); err != nil {
					return err
				}
			} else {
				r.scheduler.Upsert(entry)
			}
			previous = sandbox.ID
		}
		if len(page) < r.pageSize {
			return nil
		}
		afterID = page[len(page)-1].ID
	}
	return errors.New("TTL recovery exceeded maximum page count")
}
