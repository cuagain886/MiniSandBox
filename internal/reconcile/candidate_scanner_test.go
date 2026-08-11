package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

type candidateSweepFunc func(context.Context, time.Time, CandidatePageConsumer) error

func (f candidateSweepFunc) Sweep(ctx context.Context, now time.Time, consume CandidatePageConsumer) error {
	return f(ctx, now, consume)
}

func fixedCandidateSweep(pages ...[]domain.Sandbox) candidateSweepFunc {
	return func(ctx context.Context, _ time.Time, consume CandidatePageConsumer) error {
		for _, page := range pages {
			if err := consume(ctx, page); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestCandidateScannerWakesMultiplePagesAndMergesDuplicates 验证多页 ID 逐个提交且异常重复不放大队列。
func TestCandidateScannerWakesMultiplePagesAndMergesDuplicates(t *testing.T) {
	scanner, _ := NewCandidateScanner(
		fixedCandidateSweep(candidatePage("a", "b"), candidatePage("b", "c")),
		func(_ context.Context, id string) error {
			return nil
		},
	)
	var calls []string
	scanner.wake = func(_ context.Context, id string) error { calls = append(calls, id); return nil }
	result, err := scanner.ScanOnce(context.Background(), time.Now())
	if err != nil || !reflect.DeepEqual(calls, []string{"a", "b", "c"}) ||
		result != (CandidateScanResult{Discovered: 3, Submitted: 3, Duplicates: 1}) {
		t.Fatalf("scan result=%#v calls=%v err=%v", result, calls, err)
	}
}

// TestCandidateScannerSubmitsSharedWakeQueue 验证 scanner 只向既有合并队列提交 ID。
func TestCandidateScannerSubmitsSharedWakeQueue(t *testing.T) {
	queue := NewWakeQueue()
	scanner, _ := NewCandidateScanner(fixedCandidateSweep(candidatePage("a", "b", "c")), func(_ context.Context, id string) error {
		queue.Wake(id)
		return nil
	})
	if result, err := scanner.ScanOnce(context.Background(), time.Now()); err != nil || result.Submitted != 3 || queue.Len() != 3 {
		t.Fatalf("queue scan=%#v len=%d err=%v", result, queue.Len(), err)
	}
}

// TestCandidateScannerContinuesAfterWakeFailure 验证 queue closed 等单项失败被安全聚合且下一轮可重试。
func TestCandidateScannerContinuesAfterWakeFailure(t *testing.T) {
	secret := errors.New("queue closed at C:/secret/path")
	attempt := 0
	var calls []string
	scanner, _ := NewCandidateScanner(fixedCandidateSweep(candidatePage("a", "b")), func(_ context.Context, id string) error {
		calls = append(calls, id)
		if attempt == 0 && id == "a" {
			return secret
		}
		return nil
	})
	first, err := scanner.ScanOnce(context.Background(), time.Now())
	var wakeErr *CandidateWakeError
	if !errors.As(err, &wakeErr) || wakeErr.Failures != 1 || strings.Contains(err.Error(), "secret") ||
		first.Submitted != 1 || first.WakeFailures != 1 || !reflect.DeepEqual(calls, []string{"a", "b"}) {
		t.Fatalf("first scan=%#v calls=%v err=%v", first, calls, err)
	}
	attempt++
	calls = nil
	second, err := scanner.ScanOnce(context.Background(), time.Now())
	if err != nil || second.Submitted != 2 || !reflect.DeepEqual(calls, []string{"a", "b"}) {
		t.Fatalf("retry scan=%#v calls=%v err=%v", second, calls, err)
	}
}

// TestCandidateScannerPropagatesStoreError 验证 sweeper/Store 错误直接终止当前轮。
func TestCandidateScannerPropagatesStoreError(t *testing.T) {
	injected := errors.New("store unavailable")
	scanner, _ := NewCandidateScanner(candidateSweepFunc(func(context.Context, time.Time, CandidatePageConsumer) error {
		return injected
	}), func(context.Context, string) error { return nil })
	if _, err := scanner.ScanOnce(context.Background(), time.Now()); !errors.Is(err, injected) {
		t.Fatalf("store error: %v", err)
	}
}

// TestCandidateScannerStopsCurrentPageOnCancel 验证 cancel 后不再 Wake 后续 ID。
func TestCandidateScannerStopsCurrentPageOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls []string
	scanner, _ := NewCandidateScanner(fixedCandidateSweep(candidatePage("a", "b", "c")), func(_ context.Context, id string) error {
		calls = append(calls, id)
		cancel()
		return nil
	})
	result, err := scanner.ScanOnce(ctx, time.Now())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(calls, []string{"a"}) || result.Submitted != 1 {
		t.Fatalf("cancel scan=%#v calls=%v err=%v", result, calls, err)
	}
}
