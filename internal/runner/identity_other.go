//go:build !linux

package runner

import (
	"errors"
	"net"

	"minisandbox/internal/runnerbootstrap"
)

// DropPrivileges 在非 Linux 平台关闭 listener 并明确失败，不能用开发机
// 身份模型替代生产 Linux setgroups/setgid/setuid 语义。
func DropPrivileges(listener net.Listener, _ runnerbootstrap.Identity) error {
	if listener != nil {
		_ = listener.Close()
	}
	return errors.New("runner privilege drop requires Linux")
}
