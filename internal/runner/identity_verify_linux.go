//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"minisandbox/internal/runnerbootstrap"
)

const (
	prGetDumpable = 3
	prSetDumpable = 4
)

type restrictedIdentityOps struct {
	setDumpable func(uintptr) error
	getDumpable func() (uintptr, error)
	geteuid     func() int
	getegid     func() int
	readStatus  func() ([]byte, error)
}

var linuxRestrictedIdentityOps = restrictedIdentityOps{
	setDumpable: func(value uintptr) error {
		_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetDumpable, value, 0, 0, 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	},
	getDumpable: func() (uintptr, error) {
		value, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prGetDumpable, 0, 0, 0, 0, 0)
		if errno != 0 {
			return 0, errno
		}
		return value, nil
	},
	geteuid: syscall.Geteuid,
	getegid: syscall.Getegid,
	readStatus: func() ([]byte, error) {
		return os.ReadFile("/proc/self/status")
	},
}

type restrictedProcessStatus struct {
	effectiveUID uint32
	effectiveGID uint32
	capEff       uint64
}

// VerifyRestrictedIdentity 在 setuid 后设置不可转储状态，并主动证明实际身份、
// effective capability 与 dumpable 状态符合安全边界。
func VerifyRestrictedIdentity(identity runnerbootstrap.Identity) error {
	return verifyRestrictedIdentity(identity, linuxRestrictedIdentityOps)
}

func verifyRestrictedIdentity(
	identity runnerbootstrap.Identity,
	ops restrictedIdentityOps,
) error {
	if identity.ExecutionUID == 0 || identity.ExecutionGID == 0 {
		return errors.New("runner restricted identity must be non-root")
	}
	if err := ops.setDumpable(0); err != nil {
		return fmt.Errorf("disable runner dumpability: %w", err)
	}
	if uint32(ops.geteuid()) != identity.ExecutionUID || uint32(ops.getegid()) != identity.ExecutionGID {
		return errors.New("runner effective identity verification failed")
	}
	content, err := ops.readStatus()
	if err != nil {
		return fmt.Errorf("read runner process status: %w", err)
	}
	status, err := parseRestrictedProcessStatus(content)
	if err != nil {
		return err
	}
	if status.effectiveUID != identity.ExecutionUID || status.effectiveGID != identity.ExecutionGID {
		return errors.New("runner proc identity verification failed")
	}
	if status.capEff != 0 {
		return errors.New("runner effective capabilities are not empty")
	}
	dumpable, err := ops.getDumpable()
	if err != nil {
		return fmt.Errorf("read runner dumpability: %w", err)
	}
	if dumpable != 0 {
		return errors.New("runner remains dumpable")
	}
	return nil
}

func parseRestrictedProcessStatus(content []byte) (restrictedProcessStatus, error) {
	var result restrictedProcessStatus
	seen := map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "Uid:":
			if len(fields) != 5 || seen["uid"] {
				return restrictedProcessStatus{}, errors.New("runner proc Uid field is invalid")
			}
			value, err := strconv.ParseUint(fields[2], 10, 32)
			if err != nil {
				return restrictedProcessStatus{}, errors.New("runner proc Uid field is invalid")
			}
			result.effectiveUID = uint32(value)
			seen["uid"] = true
		case "Gid:":
			if len(fields) != 5 || seen["gid"] {
				return restrictedProcessStatus{}, errors.New("runner proc Gid field is invalid")
			}
			value, err := strconv.ParseUint(fields[2], 10, 32)
			if err != nil {
				return restrictedProcessStatus{}, errors.New("runner proc Gid field is invalid")
			}
			result.effectiveGID = uint32(value)
			seen["gid"] = true
		case "CapEff:":
			if len(fields) != 2 || seen["capeff"] {
				return restrictedProcessStatus{}, errors.New("runner proc CapEff field is invalid")
			}
			value, err := strconv.ParseUint(fields[1], 16, 64)
			if err != nil {
				return restrictedProcessStatus{}, errors.New("runner proc CapEff field is invalid")
			}
			result.capEff = value
			seen["capeff"] = true
		}
	}
	if !seen["uid"] || !seen["gid"] || !seen["capeff"] {
		return restrictedProcessStatus{}, errors.New("runner proc status is missing required identity fields")
	}
	return result, nil
}
