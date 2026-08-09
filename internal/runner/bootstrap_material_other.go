//go:build !linux

package runner

import (
	"errors"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// LoadBootstrapMaterial 在非 Linux 平台明确拒绝生产 runner bootstrap。
func LoadBootstrapMaterial(string) (runnerbootstrap.Config, runnerauth.Token, error) {
	return runnerbootstrap.Config{}, runnerauth.Token{}, errors.New("runner bootstrap requires Linux")
}
