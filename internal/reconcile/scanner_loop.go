package reconcile

import (
	"context"
	"errors"
	"time"
)

// CandidateScanOnce 是周期 loop 所需的单轮扫描端口。
type CandidateScanOnce interface {
	// ScanOnce 在给定时间边界执行一轮扫描。
	ScanOnce(context.Context, time.Time) (CandidateScanResult, error)
}

// ScannerLoop 串行运行 candidate scanner，并在轮次之间加入有界对称 jitter。
type ScannerLoop struct {
	scanner  CandidateScanOnce
	clock    Clock
	random   Random
	interval time.Duration
	jitter   time.Duration
	report   ErrorReporter
}

// NewScannerLoop 创建启动即扫描且不会重入的周期 loop。
func NewScannerLoop(scanner CandidateScanOnce, clock Clock, random Random, interval, jitter time.Duration, report ErrorReporter) (*ScannerLoop, error) {
	if scanner == nil || clock == nil || random == nil || interval <= 0 || jitter < 0 || jitter >= interval {
		return nil, errors.New("invalid scanner loop configuration")
	}
	return &ScannerLoop{scanner: scanner, clock: clock, random: random, interval: interval, jitter: jitter, report: report}, nil
}

// Run 串行执行扫描直到 server lifetime context 取消。
func (l *ScannerLoop) Run(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		l.scanSafely(ctx)
		if err := ctx.Err(); err != nil {
			return
		}
		delay := l.nextDelay()
		timer := l.clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}

func (l *ScannerLoop) scanSafely(ctx context.Context) {
	defer func() {
		if recover() != nil {
			l.reportError(errors.New("candidate scanner panicked"))
		}
	}()
	if _, err := l.scanner.ScanOnce(ctx, l.clock.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
		l.reportError(err)
	}
}

func (l *ScannerLoop) nextDelay() time.Duration {
	if l.jitter == 0 {
		return l.interval
	}
	width := int64(l.jitter)*2 + 1
	value, err := l.random.Int64N(width)
	if err != nil {
		l.reportError(errors.New("candidate scanner jitter unavailable"))
		return l.interval
	}
	return l.interval - l.jitter + time.Duration(value)
}

func (l *ScannerLoop) reportError(err error) {
	if l.report == nil {
		return
	}
	defer func() { _ = recover() }()
	l.report(err)
}
