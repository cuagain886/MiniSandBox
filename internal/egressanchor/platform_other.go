//go:build !linux

package egressanchor

import "errors"

// LinuxPlatform 在非 Linux 构建中只提供明确的 unsupported 实现，防止测试主机
// 被误当作具有 netns/capability 语义。
type LinuxPlatform struct{}

// NetworkNamespace 在非 Linux 平台始终拒绝。
func (LinuxPlatform) NetworkNamespace() (string, error) {
	return "", errors.New("egress anchor requires Linux")
}

// DropPrivileges 在非 Linux 平台始终拒绝。
func (LinuxPlatform) DropPrivileges(uint32, uint32) error {
	return errors.New("egress anchor requires Linux")
}

// Snapshot 在非 Linux 平台始终拒绝。
func (LinuxPlatform) Snapshot() (Snapshot, error) {
	return Snapshot{}, errors.New("egress anchor requires Linux")
}
