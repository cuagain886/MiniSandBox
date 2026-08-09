//go:build linux

package egressanchor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// LinuxPlatform 使用 Linux procfs、credential 与 capability 系统调用实现永久降权。
type LinuxPlatform struct{}

// NetworkNamespace 从 /proc/self/ns/net stat 取得 device 与 inode，不解析符号链接文本。
func (LinuxPlatform) NetworkNamespace() (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat("/proc/self/ns/net", &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("linux-netns:%d:%d", uint64(stat.Dev), stat.Ino), nil
}

// DropPrivileges 清空 bootstrap root 的附加组、切换固定非 root 身份、清空三组
// capability 并设置 no_new_privs；SETGID/SETUID 只服务于这次不可逆身份切换，
// 标准库 syscall 凭据封装会同步 Go runtime 的全部 OS thread，避免只降权当前
// thread 后由其他 thread 保留 root 身份；任一步失败都阻止 Ready。
func (LinuxPlatform) DropPrivileges(uid, gid uint32) error {
	if uid == 0 || gid == 0 {
		return errors.New("anchor identity must be non-root")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	if len(groups) != 0 {
		if err := syscall.Setgroups(nil); err != nil {
			return err
		}
	}
	if os.Getegid() != int(gid) {
		if err := syscall.Setresgid(int(gid), int(gid), int(gid)); err != nil {
			return err
		}
	}
	if os.Geteuid() != int(uid) {
		if err := syscall.Setresuid(int(uid), int(uid), int(uid)); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return nil
}

// Snapshot 从 /proc/self/status 回读 capability 与附加组，避免只相信降权调用返回值。
func (LinuxPlatform) Snapshot() (Snapshot, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if found {
			values[key] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return Snapshot{}, err
	}
	effective, err := parseCapability(values["CapEff"])
	if err != nil {
		return Snapshot{}, err
	}
	permitted, err := parseCapability(values["CapPrm"])
	if err != nil {
		return Snapshot{}, err
	}
	ambient, err := parseCapability(values["CapAmb"])
	if err != nil {
		return Snapshot{}, err
	}
	noNewPrivileges, err := parseNoNewPrivileges(values["NoNewPrivs"])
	if err != nil {
		return Snapshot{}, err
	}
	groups, err := parseGroups(values["Groups"])
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), SupplementaryGroups: groups,
		CapEffective: effective, CapPermitted: permitted, CapAmbient: ambient,
		NoNewPrivileges: noNewPrivileges,
	}, nil
}

func parseNoNewPrivileges(value string) (bool, error) {
	switch value {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("no_new_privs field is invalid")
	}
}

func parseCapability(value string) (uint64, error) {
	if value == "" {
		return 0, errors.New("capability field is missing")
	}
	return strconv.ParseUint(value, 16, 64)
}

func parseGroups(value string) ([]uint32, error) {
	if value == "" {
		return nil, nil
	}
	fields := strings.Fields(value)
	result := make([]uint32, len(fields))
	for index, field := range fields {
		group, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return nil, err
		}
		result[index] = uint32(group)
	}
	return result, nil
}
