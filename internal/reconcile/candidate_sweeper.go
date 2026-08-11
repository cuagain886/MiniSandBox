package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// CandidateStore 是 candidate keyset sweep 所需的最小持久化端口。
type CandidateStore interface {
	// ListReconcileCandidates 返回严格按 ID 递增的一页 due records。
	ListReconcileCandidates(context.Context, storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error)
}

// CandidatePageConsumer 接收一页已验证顺序的 due candidates。
type CandidatePageConsumer func(context.Context, []domain.Sandbox) error

// CandidateSweeper 使用固定时间边界和 keyset cursor 遍历一轮 due candidates。
type CandidateSweeper struct {
	store                CandidateStore
	pageSize             int
	maxPages             int
	pageTimeout          time.Duration
	runningCheckInterval time.Duration
}

// NewCandidateSweeper 创建不会使用 OFFSET 的有界 candidate sweeper。
func NewCandidateSweeper(store CandidateStore, pageSize, maxPages int, pageTimeout, runningCheckInterval time.Duration) (*CandidateSweeper, error) {
	if store == nil || pageSize < 1 || maxPages < 1 || pageTimeout <= 0 || runningCheckInterval <= 0 {
		return nil, errors.New("invalid candidate sweeper configuration")
	}
	return &CandidateSweeper{
		store: store, pageSize: pageSize, maxPages: maxPages,
		pageTimeout: pageTimeout, runningCheckInterval: runningCheckInterval,
	}, nil
}

// Sweep 遍历单轮快照边界；页间数据变化按 keyset 语义自然反映。
func (s *CandidateSweeper) Sweep(ctx context.Context, now time.Time, consume CandidatePageConsumer) error {
	if now.IsZero() || consume == nil {
		return errors.New("invalid candidate sweep input")
	}
	query := storeport.ReconcileCandidateQuery{
		Now: now.UTC(), RunningCutoff: now.UTC().Add(-s.runningCheckInterval), Limit: s.pageSize,
	}
	for pageNumber := 0; pageNumber < s.maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageCtx, cancel := context.WithTimeout(ctx, s.pageTimeout)
		page, err := s.store.ListReconcileCandidates(pageCtx, query)
		cancel()
		if err != nil {
			return fmt.Errorf("list reconcile candidate page: %w", err)
		}
		if len(page) > query.Limit {
			return errors.New("candidate store returned an oversized page")
		}
		previous := query.AfterID
		for _, candidate := range page {
			if candidate.ID == "" || candidate.ID <= previous {
				return errors.New("candidate store returned a non-advancing ID")
			}
			previous = candidate.ID
		}
		if len(page) == 0 {
			return nil
		}
		if err := consume(ctx, append([]domain.Sandbox(nil), page...)); err != nil {
			return err
		}
		if len(page) < query.Limit {
			return nil
		}
		query.AfterID = page[len(page)-1].ID
	}
	return errors.New("candidate sweep exceeded maximum page count")
}
