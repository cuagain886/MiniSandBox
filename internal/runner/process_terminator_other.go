//go:build !linux

package runner

import (
	"errors"
	"time"
)

func newPlatformProcessGroupTerminator(_ int, _ time.Duration, _ <-chan struct{}) (*ProcessGroupTerminator, error) {
	return nil, errors.New("process group termination is unavailable outside Linux")
}
