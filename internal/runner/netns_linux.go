//go:build linux

package runner

import (
	"fmt"
	"os"
	"syscall"
)

const currentNetNSPath = "/proc/self/ns/net"

// currentNetNSIdentity 从当前进程 netns symlink 自身的 stat device/inode
// 构造稳定身份；不能读取 symlink target 文本代替 inode 证据。
func currentNetNSIdentity() (string, error) {
	info, err := os.Stat(currentNetNSPath)
	if err != nil {
		return "", fmt.Errorf("stat current network namespace: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("stat current network namespace: unsupported metadata")
	}
	return formatNetNSIdentity(uint64(stat.Dev), stat.Ino)
}
