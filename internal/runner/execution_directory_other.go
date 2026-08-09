//go:build !linux

package runner

import (
	"errors"
	"os"

	"minisandbox/internal/runnerbootstrap"
)

// OpenManagedExecutionDirectory 在非 Linux 平台明确拒绝 Unix 目录句柄初始化。
func OpenManagedExecutionDirectory(runnerbootstrap.Config) (*os.File, error) {
	return nil, errors.New("runner execution directory requires Linux")
}
