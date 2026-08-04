//go:build !linux

package runner

import (
	"errors"
	"net"

	"minisandbox/internal/runnerbootstrap"
)

// BindManagedSocket 在非 Linux 平台明确失败；Unix ownership 与 pathname
// 权限不能由 Windows 开发机语义替代。
func BindManagedSocket(runnerbootstrap.Config) (net.Listener, error) {
	return nil, errors.New("runner managed socket requires Linux")
}
