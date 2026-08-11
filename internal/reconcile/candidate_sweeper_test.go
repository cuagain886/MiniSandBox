package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type candidateStoreFunc func(context.Context, storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error)

func (f candidateStoreFunc) ListReconcileCandidates(ctx context.Context, query storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error) {
	return f(ctx, query)
}

func candidatePage(ids ...string) []domain.Sandbox {
	page := make([]domain.Sandbox, 0, len(ids))
	for _, id := range ids {
		page = append(page, domain.Sandbox{ID: id})
	}
	return page
}

// TestCandidateSweeperEmptyAndMultiplePages 验证空库、多页和恰好整页的结束语义。
func TestCandidateSweeperEmptyAndMultiplePages(t *testing.T) {
	for _, test := range []struct {
		name  string
		pages map[string][]domain.Sandbox
		want  []string
		calls int
	}{
		{name: "empty", pages: map[string][]domain.Sandbox{"": {}}, calls: 1},
		{name: "short final", pages: map[string][]domain.Sandbox{"": candidatePage("a", "b"), "b": candidatePage("c")}, want: []string{"a", "b", "c"}, calls: 2},
		{name: "exact page", pages: map[string][]domain.Sandbox{"": candidatePage("a", "b"), "b": {}}, want: []string{"a", "b"}, calls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			store := candidateStoreFunc(func(_ context.Context, query storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error) {
				calls++
				return test.pages[query.AfterID], nil
			})
			sweeper, _ := NewCandidateSweeper(store, 2, 10, time.Second, 30*time.Second)
			var got []string
			err := sweeper.Sweep(context.Background(), time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC), func(_ context.Context, page []domain.Sandbox) error {
				for _, candidate := range page {
					got = append(got, candidate.ID)
				}
				return nil
			})
			if err != nil || calls != test.calls || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("sweep: got=%v calls=%d err=%v", got, calls, err)
			}
		})
	}
}

// TestCandidateSweeperPreservesFixedBoundaryAndMutationSemantics 验证页间插入/终止仍使用固定边界和游标。
func TestCandidateSweeperPreservesFixedBoundaryAndMutationSemantics(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var queries []storeport.ReconcileCandidateQuery
	store := candidateStoreFunc(func(_ context.Context, query storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error) {
		queries = append(queries, query)
		if query.AfterID == "" {
			return candidatePage("a", "c"), nil
		}
		// b 是页间插入但位于已消费 cursor 之前；e 已终止，adapter 只返回新出现的 d。
		return candidatePage("d"), nil
	})
	sweeper, _ := NewCandidateSweeper(store, 2, 10, time.Second, 30*time.Second)
	var got []string
	err := sweeper.Sweep(context.Background(), now, func(_ context.Context, page []domain.Sandbox) error {
		for _, candidate := range page {
			got = append(got, candidate.ID)
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "c", "d"}) || len(queries) != 2 ||
		queries[1].AfterID != "c" || !queries[0].Now.Equal(queries[1].Now) || !queries[0].RunningCutoff.Equal(queries[1].RunningCutoff) {
		t.Fatalf("mutation sweep: got=%v queries=%#v err=%v", got, queries, err)
	}
}

// TestCandidateSweeperRejectsBrokenAdapters 验证重复、倒退及不结束的 adapter 不会形成无限循环。
func TestCandidateSweeperRejectsBrokenAdapters(t *testing.T) {
	tests := []struct {
		name     string
		page     []domain.Sandbox
		maxPages int
	}{
		{name: "duplicate", page: candidatePage("a", "a"), maxPages: 3},
		{name: "backward", page: candidatePage("b", "a"), maxPages: 3},
		{name: "never ends", page: candidatePage("a"), maxPages: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := candidateStoreFunc(func(context.Context, storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error) {
				return test.page, nil
			})
			sweeper, _ := NewCandidateSweeper(store, len(test.page), test.maxPages, time.Second, time.Minute)
			err := sweeper.Sweep(context.Background(), time.Now(), func(context.Context, []domain.Sandbox) error { return nil })
			if err == nil {
				t.Fatal("broken adapter was accepted")
			}
		})
	}
}

// TestCandidateSweeperPropagatesStoreAndContextErrors 验证页面 deadline 与取消保持 typed cause。
func TestCandidateSweeperPropagatesStoreAndContextErrors(t *testing.T) {
	injected := errors.New("injected store failure")
	store := candidateStoreFunc(func(ctx context.Context, _ storeport.ReconcileCandidateQuery) ([]domain.Sandbox, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("page context has no deadline")
		}
		return nil, injected
	})
	sweeper, _ := NewCandidateSweeper(store, 2, 3, time.Minute, time.Minute)
	if err := sweeper.Sweep(context.Background(), time.Now(), func(context.Context, []domain.Sandbox) error { return nil }); !errors.Is(err, injected) {
		t.Fatalf("store error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sweeper.Sweep(ctx, time.Now(), func(context.Context, []domain.Sandbox) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
}
