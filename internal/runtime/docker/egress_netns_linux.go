//go:build linux

package docker

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type procNetNSResolver struct{}

func (procNetNSResolver) Identity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("egress sidecar PID is invalid")
	}
	var stat unix.Stat_t
	if err := unix.Stat(fmt.Sprintf("/proc/%d/ns/net", pid), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("linux-netns:%d:%d", uint64(stat.Dev), stat.Ino), nil
}
