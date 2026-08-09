package egressnft

import (
	"errors"
	"net/netip"
	"strings"

	"minisandbox/internal/egresspolicy"
)

// Compile 为已验证策略生成确定性的单 transaction nftables 输入。
// 所有动态值都先经过强类型策略校验，再以 netip 的规范字符串写入固定模板。
func Compile(policy egresspolicy.Policy) ([]byte, error) {
	if err := egresspolicy.Verify(policy); err != nil {
		return nil, errors.New("cannot compile invalid egress policy")
	}
	var builder strings.Builder
	builder.WriteString("destroy table inet " + TableName + "\n")
	builder.WriteString("table inet " + TableName + " {\n")
	builder.WriteString("  comment \"minisandbox schema=")
	builder.WriteString(intString(policy.RuleSchemaVersion))
	builder.WriteString(" policy=")
	builder.WriteString(policy.Hash)
	builder.WriteString("\"\n")
	writeSet(&builder, "ipv4_denied", "ipv4_addr", policy.IPv4)
	writeSet(&builder, "ipv6_denied", "ipv6_addr", policy.IPv6)
	builder.WriteString("  chain output {\n    type filter hook output priority filter; policy drop;\n")
	builder.WriteString("    oifname \"lo\" accept\n    ct state established,related accept\n")
	builder.WriteString("    ip daddr @ipv4_denied drop\n    ip6 daddr @ipv6_denied drop\n")
	builder.WriteString("    meta nfproto ipv4 accept\n    meta nfproto ipv6 accept\n  }\n")
	builder.WriteString("  chain input {\n    type filter hook input priority filter; policy drop;\n")
	builder.WriteString("    iifname \"lo\" accept\n    ct state established,related accept\n")
	builder.WriteString("    ip protocol icmp icmp type { destination-unreachable, time-exceeded, parameter-problem } accept\n")
	builder.WriteString("    ip6 nexthdr ipv6-icmp icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert } accept\n  }\n")
	builder.WriteString("  chain forward {\n    type filter hook forward priority filter; policy drop;\n  }\n}\n")
	return []byte(builder.String()), nil
}

func writeSet(builder *strings.Builder, name, addressType string, prefixes []netip.Prefix) {
	builder.WriteString("  set " + name + " { type " + addressType + "; flags interval; elements = { ")
	for index, prefix := range prefixes {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(prefix.String())
	}
	builder.WriteString(" } }\n")
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
