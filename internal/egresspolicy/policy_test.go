package egresspolicy

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestBuildCanonicalPolicy 验证内置基线、运维追加项和 Docker 网络事实会合并为
// 稳定、规范化且按地址族分离的拒绝集合。
func TestBuildCanonicalPolicy(t *testing.T) {
	policy, err := Build(
		[]string{"8.8.8.129/24", "8.8.8.0/24", "2001:4860:1234::/32"},
		[]ManagedNetwork{{
			Subnets:  []string{"9.9.9.0/24", "2001:4861::/48"},
			Gateways: []string{"11.11.11.11", "2001:4862::1"},
		}},
	)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	for _, want := range []string{"10.0.0.0/8", "8.8.8.0/24", "9.9.9.0/24", "11.11.11.11/32"} {
		if !containsPrefix(policy.IPv4, want) {
			t.Fatalf("IPv4 policy does not contain %s: %v", want, policy.IPv4)
		}
	}
	for _, want := range []string{"fc00::/7", "2001:4860::/32", "2001:4861::/48", "2001:4862::1/128"} {
		if !containsPrefix(policy.IPv6, want) {
			t.Fatalf("IPv6 policy does not contain %s: %v", want, policy.IPv6)
		}
	}
	if got := prefixCount(policy.IPv4, "8.8.8.0/24"); got != 1 {
		t.Fatalf("canonical duplicate was not folded: got %d entries", got)
	}
	if policy.ProtocolVersion != CurrentProtocolVersion || policy.RuleSchemaVersion != CurrentRuleSchemaVersion {
		t.Fatalf("unexpected policy versions: %+v", policy)
	}
	if len(policy.Hash) != 64 {
		t.Fatalf("unexpected policy hash: %q", policy.Hash)
	}
}

// TestBuildStableHash 验证输入顺序、非规范 host bits 和被上级网段覆盖的条目
// 不会改变策略身份，而真实 deny set 变化一定产生新 hash。
func TestBuildStableHash(t *testing.T) {
	first, err := Build([]string{"8.8.8.1/24", "9.9.9.9/32"}, nil)
	if err != nil {
		t.Fatalf("build first policy: %v", err)
	}
	second, err := Build([]string{"9.9.9.9/32", "8.8.8.0/24", "8.8.8.8/32"}, nil)
	if err != nil {
		t.Fatalf("build equivalent policy: %v", err)
	}
	if first.Hash != second.Hash || !reflect.DeepEqual(first.IPv4, second.IPv4) {
		t.Fatalf("equivalent policies differ:\nfirst  %+v\nsecond %+v", first, second)
	}

	changed, err := Build([]string{"8.8.8.0/24", "9.9.9.10/32"}, nil)
	if err != nil {
		t.Fatalf("build changed policy: %v", err)
	}
	if changed.Hash == first.Hash {
		t.Fatal("different deny sets produced the same policy hash")
	}
}

// TestBuildKeepsMandatoryBaseline 验证空运维配置仍保留不可删除的内部网段基线。
func TestBuildKeepsMandatoryBaseline(t *testing.T) {
	policy, err := Build(nil, nil)
	if err != nil {
		t.Fatalf("build baseline policy: %v", err)
	}
	for _, want := range []string{"10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10"} {
		if !containsPrefix(append(append([]netip.Prefix(nil), policy.IPv4...), policy.IPv6...), want) {
			t.Fatalf("mandatory baseline does not contain %s", want)
		}
	}
}

// TestBuildRejectsInvalidFacts 验证平台配置和 Docker inspect 事实缺失或非法时
// fail closed，且错误不会回显完整 CIDR 输入。
func TestBuildRejectsInvalidFacts(t *testing.T) {
	const canary = "invalid-cidr-canary"
	tests := []struct {
		name       string
		additional []string
		networks   []ManagedNetwork
	}{
		{name: "empty additional CIDR", additional: []string{""}},
		{name: "invalid additional CIDR", additional: []string{canary}},
		{name: "missing managed subnet", networks: []ManagedNetwork{{Gateways: []string{"8.8.8.8"}}}},
		{name: "invalid managed subnet", networks: []ManagedNetwork{{Subnets: []string{canary}}}},
		{name: "invalid managed gateway", networks: []ManagedNetwork{{Subnets: []string{"8.8.8.0/24"}, Gateways: []string{canary}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Build(test.additional, test.networks)
			if err == nil {
				t.Fatal("expected build error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaks rejected network value: %v", err)
			}
		})
	}
}

// TestPolicyStringRedactsCIDRs 验证日志字符串只输出策略身份和计数。
func TestPolicyStringRedactsCIDRs(t *testing.T) {
	policy, err := Build([]string{"8.8.8.0/24"}, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	got := policy.String()
	if strings.Contains(got, "8.8.8.0/24") || !strings.Contains(got, policy.Hash) {
		t.Fatalf("unsafe policy string: %s", got)
	}
}

func containsPrefix(prefixes []netip.Prefix, value string) bool {
	want := netip.MustParsePrefix(value)
	for _, prefix := range prefixes {
		if prefix == want {
			return true
		}
	}
	return false
}

func prefixCount(prefixes []netip.Prefix, value string) int {
	want := netip.MustParsePrefix(value)
	count := 0
	for _, prefix := range prefixes {
		if prefix == want {
			count++
		}
	}
	return count
}
