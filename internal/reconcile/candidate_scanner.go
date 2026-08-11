package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
)

// CandidateSweep 是 scanner 所需的一轮 candidate 遍历能力。
type CandidateSweep interface {
	// Sweep 按页调用 consumer，且不直接执行 reconcile 或 Wake。
	Sweep(context.Context, time.Time, CandidatePageConsumer) error
}

// CandidateWakeFunc 尝试把一个 due ID 提交给合并队列。
type CandidateWakeFunc func(context.Context, string) error

// CandidateScanResult 汇总一次 ScanOnce 的安全计数，不包含底层错误文本。
type CandidateScanResult struct {
	// Discovered 是 sweep 返回的非重复 ID 数。
	Discovered int
	// Submitted 是未返回错误的 Wake 次数。
	Submitted int
	// Duplicates 是异常上游在同轮重复提供且被 scanner 合并的 ID 数。
	Duplicates int
	// WakeFailures 是 Wake 返回错误但扫描继续的 ID 数。
	WakeFailures int
}

// CandidateWakeError 表示一次扫描中存在一个或多个 Wake 失败。
type CandidateWakeError struct{ Failures int }

// Error 返回固定安全文本和失败计数，不包含队列内部错误。
func (e *CandidateWakeError) Error() string {
	return fmt.Sprintf("candidate wake failed for %d item(s)", e.Failures)
}

// CandidateScanner 把 due candidate 提交给合并 WakeQueue，不直接调用 Reconcile。
type CandidateScanner struct {
	sweeper CandidateSweep
	wake    CandidateWakeFunc
}

// NewCandidateScanner 创建单轮 scanner。
func NewCandidateScanner(sweeper CandidateSweep, wake CandidateWakeFunc) (*CandidateScanner, error) {
	if sweeper == nil || wake == nil {
		return nil, errors.New("candidate scanner dependencies must not be nil")
	}
	return &CandidateScanner{sweeper: sweeper, wake: wake}, nil
}

// ScanOnce 遍历并唤醒当前 due candidates；单个 Wake 失败不阻断其他 ID。
func (s *CandidateScanner) ScanOnce(ctx context.Context, now time.Time) (CandidateScanResult, error) {
	result := CandidateScanResult{}
	seen := make(map[string]struct{})
	err := s.sweeper.Sweep(ctx, now, func(ctx context.Context, page []domain.Sandbox) error {
		for _, candidate := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, exists := seen[candidate.ID]; exists {
				result.Duplicates++
				continue
			}
			seen[candidate.ID] = struct{}{}
			result.Discovered++
			if err := s.wake(ctx, candidate.ID); err != nil {
				result.WakeFailures++
				continue
			}
			result.Submitted++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.WakeFailures > 0 {
		return result, &CandidateWakeError{Failures: result.WakeFailures}
	}
	return result, nil
}
