//go:build !linux

package runner

import (
	"errors"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// LoadBootstrapMaterial 在非 Linux 平台明确拒绝生产 runner bootstrap。
func LoadBootstrapMaterial(string) (runnerbootstrap.Config, runnerauth.Token, error) {
	return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap requires Linux")
}

// WaitLoadBootstrapMaterial 在非 Linux 平台明确拒绝生产 runner bootstrap。
func WaitLoadBootstrapMaterial(string, time.Duration) (runnerbootstrap.Config, runnerauth.Token, error) {
	return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap requires Linux")
}

// RestoreBootstrapDirectoryOwner 在非 Linux 平台明确拒绝 Unix owner 恢复。
func RestoreBootstrapDirectoryOwner(runnerbootstrap.Config) error {
	return errors.New("runner bootstrap requires Linux")
}
