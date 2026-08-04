//go:build linux

package runner

import (
	"errors"
	"net"
	"syscall"

	"minisandbox/internal/runnerbootstrap"
)

const prSetKeepCaps = 8

type identityOps struct {
	disableKeepCaps func() error
	setgroups       func([]int) error
	setgid          func(int) error
	setuid          func(int) error
}

var linuxIdentityOps = identityOps{
	disableKeepCaps: func() error {
		_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetKeepCaps, 0, 0, 0, 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	},
	setgroups: syscall.Setgroups,
	setgid:    syscall.Setgid,
	setuid:    syscall.Setuid,
}

// DropPrivileges 清空 supplementary groups，并把 runner 永久降为固定
// execution GID/UID；失败时立即关闭已绑定 listener。
//
// 成功时 listener 保持打开，供已降权 runner 继续 accept；本函数不启动用户
// 命令，也不尝试保留或恢复 capability。
func DropPrivileges(listener net.Listener, identity runnerbootstrap.Identity) (err error) {
	if listener == nil {
		return errors.New("runner listener is required before dropping privileges")
	}
	defer func() {
		if err != nil {
			_ = listener.Close()
		}
	}()
	return dropPrivileges(identity, linuxIdentityOps)
}

func dropPrivileges(identity runnerbootstrap.Identity, ops identityOps) error {
	if identity.ExecutionUID == 0 || identity.ExecutionGID == 0 {
		return errors.New("runner execution identity must be non-root")
	}
	if identity.ExecutionUID == identity.SocketOwnerUID ||
		identity.ExecutionGID == identity.SocketOwnerGID {
		return errors.New("runner execution identity must differ from socket owner")
	}
	// 显式关闭 keepcaps 后，再按不可交换的 groups → gid → uid 顺序降权；
	// setuid 一旦成功，后续代码不得再依赖任何特权操作。
	if err := ops.disableKeepCaps(); err != nil {
		return err
	}
	if err := ops.setgroups(nil); err != nil {
		return err
	}
	if err := ops.setgid(int(identity.ExecutionGID)); err != nil {
		return err
	}
	if err := ops.setuid(int(identity.ExecutionUID)); err != nil {
		return err
	}
	return nil
}
