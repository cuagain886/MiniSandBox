//go:build linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"minisandbox/internal/runnerbootstrap"
)

const (
	restrictedHelperEnv = "MINISANDBOX_RUNNER_RESTRICTED_HELPER"
	environProbePIDEnv  = "MINISANDBOX_RUNNER_ENVIRON_PROBE_PID"
)

// TestParseRestrictedProcessStatus 验证真实格式只提取 effective UID/GID 与
// CapEff，并拒绝缺失、重复、非法数值和 capability 残留。
func TestParseRestrictedProcessStatus(t *testing.T) {
	valid := []byte("Name:\trunnerd\nUid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t0000000000000000\n")
	status, err := parseRestrictedProcessStatus(valid)
	if err != nil || status.effectiveUID != 1000 || status.effectiveGID != 1001 || status.capEff != 0 {
		t.Fatalf("parse valid status: %+v, %v", status, err)
	}
	for _, fixture := range [][]byte{
		[]byte("Gid:\t1001\t1001\t1001\t1001\nCapEff:\t0\n"),
		[]byte("Uid:\tx\tx\tx\tx\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t0\n"),
		[]byte("Uid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\txyz\n"),
		[]byte("Uid:\t1000\t1000\t1000\t1000\nUid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t0\n"),
	} {
		if _, err := parseRestrictedProcessStatus(fixture); err == nil {
			t.Fatalf("invalid proc status accepted: %q", fixture)
		}
	}
}

// TestVerifyRestrictedIdentityFailures 逐项验证身份、capability、dumpable 和
// procfs 失败都会使 bootstrap 失败。
func TestVerifyRestrictedIdentityFailures(t *testing.T) {
	identity := runnerbootstrap.Identity{ExecutionUID: 1000, ExecutionGID: 1001}
	validStatus := []byte("Uid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t0\n")
	base := func() restrictedIdentityOps {
		return restrictedIdentityOps{
			setDumpable: func(uintptr) error { return nil },
			getDumpable: func() (uintptr, error) { return 0, nil },
			geteuid:     func() int { return 1000 },
			getegid:     func() int { return 1001 },
			readStatus:  func() ([]byte, error) { return validStatus, nil },
		}
	}
	tests := []struct {
		name   string
		mutate func(*restrictedIdentityOps)
	}{
		{"set dumpable", func(o *restrictedIdentityOps) { o.setDumpable = func(uintptr) error { return errors.New("fail") } }},
		{"effective uid", func(o *restrictedIdentityOps) { o.geteuid = func() int { return 2000 } }},
		{"read status", func(o *restrictedIdentityOps) {
			o.readStatus = func() ([]byte, error) { return nil, errors.New("fail") }
		}},
		{"proc identity", func(o *restrictedIdentityOps) {
			o.readStatus = func() ([]byte, error) {
				return []byte("Uid:\t2000\t2000\t2000\t2000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t0\n"), nil
			}
		}},
		{"capability remains", func(o *restrictedIdentityOps) {
			o.readStatus = func() ([]byte, error) {
				return []byte("Uid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t1\n"), nil
			}
		}},
		{"get dumpable", func(o *restrictedIdentityOps) {
			o.getDumpable = func() (uintptr, error) { return 0, errors.New("fail") }
		}},
		{"dumpable remains", func(o *restrictedIdentityOps) { o.getDumpable = func() (uintptr, error) { return 1, nil } }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			test.mutate(&ops)
			if err := verifyRestrictedIdentity(identity, ops); err == nil {
				t.Fatal("unsafe restricted identity accepted")
			}
		})
	}
	if err := verifyRestrictedIdentity(identity, base()); err != nil {
		t.Fatalf("valid restricted identity rejected: %v", err)
	}
}

// TestVerifyRestrictedIdentityRootHelper 在 root Linux 环境真实执行降权和
// verifier，并证明同 UID helper 可发 signal 0、但不能读取 runner environ。
func TestVerifyRestrictedIdentityRootHelper(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root Linux helper; run in the privileged integration environment")
	}
	command := exec.Command(os.Args[0], "-test.run=TestVerifyRestrictedIdentityChild")
	command.Env = append(os.Environ(), restrictedHelperEnv+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("restricted child: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "restricted-verified") {
		t.Fatalf("missing restricted evidence: %q", output)
	}
}

// TestVerifyRestrictedIdentityChild 是真实 verifier 的隔离降权入口。
func TestVerifyRestrictedIdentityChild(t *testing.T) {
	if os.Getenv(restrictedHelperEnv) != "1" {
		return
	}
	directory := filepath.Join(os.TempDir(), "minisandbox-verify-"+strconv.Itoa(os.Getpid()))
	if err := os.Mkdir(directory, 0o700); err != nil {
		os.Exit(10)
	}
	identity := runnerbootstrap.Identity{ExecutionUID: 65534, ExecutionGID: 65534, SocketOwnerUID: 0, SocketOwnerGID: 0}
	listener, err := bindManagedSocketAt(directory, filepath.Join(directory, "runner.sock"), identity)
	if err != nil {
		os.Exit(11)
	}
	if err := DropPrivileges(listener, identity); err != nil {
		os.Exit(12)
	}
	if err := VerifyRestrictedIdentity(identity); err != nil {
		os.Exit(13)
	}
	probe := exec.Command(os.Args[0], "-test.run=TestRestrictedEnvironProbe")
	probe.Env = append(os.Environ(), environProbePIDEnv+"="+strconv.Itoa(os.Getpid()))
	if err := probe.Run(); err != nil {
		os.Exit(14)
	}
	_, _ = os.Stdout.WriteString("restricted-verified\n")
}

// TestRestrictedEnvironProbe 模拟同 execution UID 的用户进程：signal 0 仍被
// G3 接受，但读取不可转储 runner 的 environ 必须失败。
func TestRestrictedEnvironProbe(t *testing.T) {
	raw := os.Getenv(environProbePIDEnv)
	if raw == "" {
		return
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || syscall.Kill(pid, 0) != nil {
		os.Exit(21)
	}
	_, err = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err == nil || !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		os.Exit(22)
	}
}
