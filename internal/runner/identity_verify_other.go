//go:build !linux

package runner

import (
	"errors"

	"minisandbox/internal/runnerbootstrap"
)

// VerifyRestrictedIdentity 在非 Linux 平台明确失败；capability、procfs 与
// prctl dumpable 证据不能被其他平台的近似状态替代。
func VerifyRestrictedIdentity(runnerbootstrap.Identity) error {
	return errors.New("runner restricted identity verification requires Linux")
}
