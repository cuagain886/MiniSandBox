package egressnft

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"

	"minisandbox/internal/egresspolicy"
)

// Executor 是 nft 子进程的最小端口；实现必须直接传 argv，禁止经过 shell。
type Executor interface {
	// Run 执行固定程序与参数，并把 data 作为 stdin；返回合并后的有界诊断输出。
	Run(context.Context, string, []string, []byte) ([]byte, error)
}

// OSExecutor 使用 os/exec 直接启动 nft，不解释 shell 元字符。
type OSExecutor struct{}

const maxNFTDiagnosticBytes = 4096

// Run 执行一次外部命令；调用者只传固定 nft 程序和固定 argv。
func (OSExecutor) Run(ctx context.Context, name string, args []string, data []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = bytes.NewReader(data)
	diagnostic := &limitedBuffer{maximum: maxNFTDiagnosticBytes}
	command.Stdout = diagnostic
	command.Stderr = diagnostic
	err := command.Run()
	return diagnostic.Bytes(), err
}

type limitedBuffer struct {
	data    []byte
	maximum int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := buffer.maximum - len(buffer.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		buffer.data = append(buffer.data, data...)
	}
	return originalLength, nil
}

func (buffer *limitedBuffer) Bytes() []byte { return append([]byte(nil), buffer.data...) }

// Install 以一次 nft -f - 原子提交规则，并通过只读 list 回验 table、chain、版本、
// hash、IPv4/IPv6 set 和全部规范化元素。失败时保留 fail-closed 状态且不执行清空。
func Install(ctx context.Context, executor Executor, policy egresspolicy.Policy) error {
	if executor == nil {
		return errors.New("egress nft executor is required")
	}
	rules, err := Compile(policy)
	if err != nil {
		return err
	}
	if _, err := executor.Run(ctx, "nft", []string{"-f", "-"}, rules); err != nil {
		return errors.New("install egress nft transaction")
	}
	observed, err := executor.Run(ctx, "nft", []string{"list", "table", "inet", TableName}, nil)
	if err != nil {
		return errors.New("read back egress nft table")
	}
	if err := verifyReadback(string(observed), policy); err != nil {
		return err
	}
	return nil
}

func verifyReadback(observed string, policy egresspolicy.Policy) error {
	markers := []string{
		"table inet " + TableName,
		"chain output", "chain input", "chain forward",
		"set ipv4_denied", "set ipv6_denied",
		"schema=" + intString(policy.RuleSchemaVersion), "policy=" + policy.Hash,
	}
	for _, prefix := range append(append([]netip.Prefix(nil), policy.IPv4...), policy.IPv6...) {
		markers = append(markers, readbackPrefixMarker(prefix))
	}
	for _, marker := range markers {
		if !strings.Contains(observed, marker) {
			return errors.New("egress nft readback does not match policy")
		}
	}
	return nil
}

func readbackPrefixMarker(prefix netip.Prefix) string {
	if prefix.IsValid() && prefix.Bits() == prefix.Addr().BitLen() {
		return prefix.Addr().String()
	}
	return prefix.String()
}
