package egressnft

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// TestCompileStaticRules 验证规则顺序、双栈 deny set、固定 INPUT allowlist 和
// FORWARD 默认拒绝均被写入同一个确定性 transaction。
func TestCompileStaticRules(t *testing.T) {
	policy := testPolicy(t)
	first, err := Compile(policy)
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	second, err := Compile(policy)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("rule compilation is not deterministic")
	}
	rules := string(first)
	markers := []string{
		"table inet minisandbox_egress",
		"set ipv4_denied { type ipv4_addr; flags interval;",
		"set ipv6_denied { type ipv6_addr; flags interval;",
		"oifname \"lo\" accept",
		"ct state established,related accept",
		"ip daddr @ipv4_denied drop",
		"ip6 daddr @ipv6_denied drop",
		"meta nfproto ipv4 accept",
		"meta nfproto ipv6 accept",
		"icmp type { destination-unreachable, time-exceeded, parameter-problem } accept",
		"icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert } accept",
		"chain forward",
	}
	for _, marker := range markers {
		if !strings.Contains(rules, marker) {
			t.Fatalf("compiled rules do not contain %q", marker)
		}
	}
	if strings.Index(rules, "oifname \"lo\"") > strings.Index(rules, "@ipv4_denied") ||
		strings.Index(rules, "@ipv4_denied") > strings.Index(rules, "meta nfproto ipv4 accept") {
		t.Fatal("OUTPUT rule ordering is not fail closed")
	}
}

// TestInstallAtomicAndVerify 验证安装只调用固定 nft argv，并在提交后执行只读回验；
// 同一策略重复执行保持相同 transaction。
func TestInstallAtomicAndVerify(t *testing.T) {
	policy := testPolicy(t)
	rules, err := Compile(policy)
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	executor := &recordingExecutor{readback: rules}
	for range 2 {
		if err := Install(context.Background(), executor, policy); err != nil {
			t.Fatalf("install policy: %v", err)
		}
	}
	if len(executor.calls) != 4 {
		t.Fatalf("unexpected nft call count: %d", len(executor.calls))
	}
	for index := 0; index < len(executor.calls); index += 2 {
		install := executor.calls[index]
		if install.name != "nft" || strings.Join(install.args, " ") != "-f -" || !bytes.Equal(install.stdin, rules) {
			t.Fatalf("unsafe install call: %+v", install)
		}
		readback := executor.calls[index+1]
		if readback.name != "nft" || strings.Join(readback.args, " ") != "list table inet minisandbox_egress" || len(readback.stdin) != 0 {
			t.Fatalf("unsafe readback call: %+v", readback)
		}
	}
}

// TestInstallFailures 验证 nft 缺失、transaction 失败和回验漂移均返回错误，且不会
// 发出清空 table 的补偿命令。
func TestInstallFailures(t *testing.T) {
	policy := testPolicy(t)
	rules, err := Compile(policy)
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	tests := []struct {
		name     string
		executor *recordingExecutor
	}{
		{name: "nft unavailable", executor: &recordingExecutor{failCall: 1}},
		{name: "transaction failed", executor: &recordingExecutor{failCall: 1}},
		{name: "readback failed", executor: &recordingExecutor{readback: rules, failCall: 2}},
		{name: "missing chain", executor: &recordingExecutor{readback: []byte(strings.Replace(string(rules), "chain forward", "chain missing", 1))}},
		{name: "missing IPv4 set", executor: &recordingExecutor{readback: []byte(strings.Replace(string(rules), "set ipv4_denied", "set missing", 1))}},
		{name: "missing IPv6 set", executor: &recordingExecutor{readback: []byte(strings.Replace(string(rules), "set ipv6_denied", "set missing", 1))}},
		{name: "hash drift", executor: &recordingExecutor{readback: []byte(strings.Replace(string(rules), policy.Hash, strings.Repeat("0", 64), 1))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Install(context.Background(), test.executor, policy); err == nil {
				t.Fatal("expected install failure")
			}
			if len(test.executor.calls) > 2 {
				t.Fatalf("failure triggered unexpected compensation: %+v", test.executor.calls)
			}
		})
	}
	if err := Install(context.Background(), nil, policy); err == nil {
		t.Fatal("nil executor accepted")
	}
}

// TestLimitedBuffer 验证外部 nft 的诊断输出不会突破 sidecar 内存边界。
func TestLimitedBuffer(t *testing.T) {
	buffer := &limitedBuffer{maximum: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("unexpected write result: written=%d err=%v", written, err)
	}
	if got := string(buffer.Bytes()); got != "abcd" {
		t.Fatalf("unexpected bounded diagnostic: %q", got)
	}
}

type executorCall struct {
	name  string
	args  []string
	stdin []byte
}

type recordingExecutor struct {
	calls    []executorCall
	readback []byte
	failCall int
}

func (executor *recordingExecutor) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	executor.calls = append(executor.calls, executorCall{name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...)})
	if executor.failCall == len(executor.calls) {
		return nil, errors.New("forced nft failure")
	}
	if len(executor.calls)%2 == 0 {
		return append([]byte(nil), executor.readback...), nil
	}
	return nil, nil
}
