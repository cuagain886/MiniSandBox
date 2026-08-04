//go:build !linux

package runner

import (
	"errors"

	"minisandbox/internal/runnerbootstrap"
)

// InitializeManagedDirectories 在非 Linux 平台明确失败；runner root bootstrap
// 依赖 Linux ownership 与 mount 语义，不能用开发机行为替代。
func InitializeManagedDirectories(runnerbootstrap.Config) error {
	return errors.New("runner managed directory bootstrap requires Linux")
}
