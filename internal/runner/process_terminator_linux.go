//go:build linux

package runner

import (
	"errors"
	"syscall"
	"time"
)

const (
	processGroupProbeInterval = 5 * time.Millisecond
	processGroupProbeTimeout  = 5 * time.Second
)

type processGroupTerminationOps struct {
	signal func(int, syscall.Signal) error
	after  func(time.Duration) <-chan time.Time
	now    func() time.Time
}

var osProcessGroupTerminationOps = processGroupTerminationOps{
	signal: syscall.Kill,
	after:  time.After,
	now:    time.Now,
}

func newPlatformProcessGroupTerminator(pgid int, grace time.Duration, reaped <-chan struct{}) (*ProcessGroupTerminator, error) {
	return newProcessGroupTerminator(pgid, grace, reaped, osProcessGroupTerminationOps)
}

func newProcessGroupTerminator(
	pgid int,
	grace time.Duration,
	reaped <-chan struct{},
	ops processGroupTerminationOps,
) (*ProcessGroupTerminator, error) {
	if pgid <= 1 || grace <= 0 || reaped == nil || ops.signal == nil || ops.after == nil || ops.now == nil {
		return nil, errors.New("process group terminator is not configured")
	}
	target := -pgid
	terminator := &ProcessGroupTerminator{done: make(chan struct{})}
	terminator.terminate = func() error {
		if err := ops.signal(target, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return ErrProcessGroupTerminationFailed
		}
		<-ops.after(grace)
		err := ops.signal(target, 0)
		switch {
		case errors.Is(err, syscall.ESRCH):
			return nil
		case err != nil:
			return ErrProcessGroupTerminationFailed
		}
		if err := ops.signal(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return ErrProcessGroupTerminationFailed
		}
		<-reaped
		deadline := ops.now().Add(processGroupProbeTimeout)
		for {
			err := ops.signal(target, 0)
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			if err != nil || !ops.now().Before(deadline) {
				return ErrProcessGroupTerminationFailed
			}
			<-ops.after(processGroupProbeInterval)
		}
	}
	return terminator, nil
}
