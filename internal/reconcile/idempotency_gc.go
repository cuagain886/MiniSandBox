package reconcile

import (
	"context"
	"errors"
	"time"

	storeport "minisandbox/internal/store"
)

// IdempotencyGCStore 是终态幂等记录回收所需的最小持久化端口。
type IdempotencyGCStore interface {
	// DeleteExpiredIdempotencyRecords 原子删除一批满足终态保留条件的记录。
	DeleteExpiredIdempotencyRecords(context.Context, storeport.IdempotencyGCQuery) (storeport.IdempotencyGCBatch, error)
}

// IdempotencyGC 按稳定 key 分页回收终态幂等记录。
type IdempotencyGC struct {
	store     IdempotencyGCStore
	retention time.Duration
	interval  time.Duration
	batchSize int
	now       func() time.Time
	report    ErrorReporter
}

// NewIdempotencyGC 创建使用 server lifetime context 的周期回收器。
func NewIdempotencyGC(store IdempotencyGCStore, retention, interval time.Duration, batchSize int, report ErrorReporter) (*IdempotencyGC, error) {
	if store == nil || retention < 24*time.Hour || interval <= 0 || batchSize < 1 {
		return nil, errors.New("invalid idempotency GC configuration")
	}
	return &IdempotencyGC{
		store: store, retention: retention, interval: interval, batchSize: batchSize,
		now: time.Now, report: report,
	}, nil
}

// SweepOnce 删除当前时刻全部已满足条件的记录；任一批失败立即结束本轮。
func (g *IdempotencyGC) SweepOnce(ctx context.Context) (int, error) {
	now := g.now().UTC()
	query := storeport.IdempotencyGCQuery{
		Now: now, TerminalRetention: g.retention, Limit: g.batchSize,
	}
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		batch, err := g.store.DeleteExpiredIdempotencyRecords(ctx, query)
		if err != nil {
			return total, err
		}
		total += batch.Deleted
		if batch.Deleted < query.Limit {
			return total, nil
		}
		if batch.LastScopeID == "" || batch.LastKey == "" ||
			batch.LastScopeID < query.AfterScopeID ||
			batch.LastScopeID == query.AfterScopeID && batch.LastKey <= query.AfterKey {
			return total, errors.New("idempotency GC store returned a non-advancing cursor")
		}
		query.AfterScopeID, query.AfterKey = batch.LastScopeID, batch.LastKey
	}
}

// Run 周期执行回收；单轮失败仅报告，下一周期会从空游标重新尝试。
func (g *IdempotencyGC) Run(ctx context.Context) {
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := g.SweepOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && g.report != nil {
				g.report(err)
			}
		}
	}
}
