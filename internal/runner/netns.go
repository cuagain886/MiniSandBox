package runner

import (
	"errors"
	"fmt"
)

// formatNetNSIdentity 将 Linux stat device/inode 编码为内部协议固定格式。
func formatNetNSIdentity(device, inode uint64) (string, error) {
	if device == 0 || inode == 0 {
		return "", errors.New("network namespace stat identity must be non-zero")
	}
	return fmt.Sprintf("linux-netns:%d:%d", device, inode), nil
}
