// Package egresspolicy 构造 Phase 2 egress sidecar 使用的不可变网络拒绝策略。
//
// 本模块只处理可信平台配置和 Docker inspect 得到的 subnet/gateway，负责
// canonicalize、去重、折叠和稳定 hash；它不生成 nft syntax、不调用 Docker，
// 也不接受公共 sandbox 或 execution 请求。
package egresspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"strconv"
)

// CurrentProtocolVersion 是 egress bootstrap 的精确协议版本。
const CurrentProtocolVersion = 1

// CurrentRuleSchemaVersion 是 immutable deny policy 的精确规则 schema。
const CurrentRuleSchemaVersion = 1

var baselineCIDRs = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32",
}

// ManagedNetwork 是 Docker inspect 提供的实际 subnet 与 gateway。
type ManagedNetwork struct {
	// Subnets 是当前受管 network 的 IPv4/IPv6 CIDR；至少包含一项。
	Subnets []string
	// Gateways 是 Docker 分配的 IPv4/IPv6 gateway address。
	Gateways []string
}

// Policy 是规范化、不可变且可稳定哈希的 deny set。
type Policy struct {
	// ProtocolVersion 必须与 sidecar bootstrap 精确一致。
	ProtocolVersion int
	// RuleSchemaVersion 必须与 nft rule compiler 精确一致。
	RuleSchemaVersion int
	// IPv4 是排序、去重并折叠后的 IPv4 prefix。
	IPv4 []netip.Prefix
	// IPv6 是排序、去重并折叠后的 IPv6 prefix。
	IPv6 []netip.Prefix
	// Hash 是版本和两个 canonical set 的 SHA-256 小写十六进制摘要。
	Hash string
}

// Build 合并内置基线、运维追加 CIDR 和实际 Docker subnet/gateway。
func Build(additionalCIDRs []string, networks []ManagedNetwork) (Policy, error) {
	values := make([]netip.Prefix, 0, len(baselineCIDRs)+len(additionalCIDRs)+len(networks)*2)
	for _, cidr := range append(append([]string(nil), baselineCIDRs...), additionalCIDRs...) {
		prefix, err := parsePrefix(cidr)
		if err != nil {
			return Policy{}, errors.New("egress deny CIDR is invalid")
		}
		values = append(values, prefix)
	}
	for _, network := range networks {
		if len(network.Subnets) == 0 {
			return Policy{}, errors.New("managed Docker network subnet is missing")
		}
		for _, subnet := range network.Subnets {
			prefix, err := parsePrefix(subnet)
			if err != nil {
				return Policy{}, errors.New("managed Docker network subnet is invalid")
			}
			values = append(values, prefix)
		}
		for _, gateway := range network.Gateways {
			address, err := netip.ParseAddr(gateway)
			if err != nil || address.Is4In6() {
				return Policy{}, errors.New("managed Docker network gateway is invalid")
			}
			values = append(values, netip.PrefixFrom(address, address.BitLen()))
		}
	}
	ipv4, ipv6 := splitAndCollapse(values)
	hash, err := policyHash(ipv4, ipv6)
	if err != nil {
		return Policy{}, errors.New("egress policy hash failed")
	}
	return Policy{ProtocolVersion: CurrentProtocolVersion, RuleSchemaVersion: CurrentRuleSchemaVersion, IPv4: ipv4, IPv6: ipv6, Hash: hash}, nil
}

func parsePrefix(value string) (netip.Prefix, error) {
	if value == "" {
		return netip.Prefix{}, errors.New("empty prefix")
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("invalid prefix")
	}
	return prefix.Masked(), nil
}

func splitAndCollapse(values []netip.Prefix) ([]netip.Prefix, []netip.Prefix) {
	var ipv4, ipv6 []netip.Prefix
	for _, value := range values {
		if value.Addr().Is4() {
			ipv4 = append(ipv4, value)
		} else {
			ipv6 = append(ipv6, value)
		}
	}
	return collapse(ipv4), collapse(ipv6)
}

func collapse(values []netip.Prefix) []netip.Prefix {
	sort.Slice(values, func(left, right int) bool {
		leftAddress, rightAddress := values[left].Addr(), values[right].Addr()
		if leftAddress == rightAddress {
			return values[left].Bits() < values[right].Bits()
		}
		return leftAddress.Less(rightAddress)
	})
	result := make([]netip.Prefix, 0, len(values))
	for _, candidate := range values {
		covered := false
		for _, existing := range result {
			if existing.Bits() <= candidate.Bits() && existing.Contains(candidate.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, candidate)
		}
	}
	return result
}

func policyHash(ipv4, ipv6 []netip.Prefix) (string, error) {
	wire := struct {
		Protocol int      `json:"protocol"`
		Schema   int      `json:"schema"`
		IPv4     []string `json:"ipv4"`
		IPv6     []string `json:"ipv6"`
	}{Protocol: CurrentProtocolVersion, Schema: CurrentRuleSchemaVersion, IPv4: prefixStrings(ipv4), IPv6: prefixStrings(ipv6)}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func prefixStrings(prefixes []netip.Prefix) []string {
	result := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		result[index] = prefix.String()
	}
	return result
}

// String 返回安全摘要，不输出完整运行时 CIDR policy。
func (p Policy) String() string {
	return "egresspolicy.Policy{hash=" + p.Hash + ",ipv4_count=" + strconv.Itoa(len(p.IPv4)) + ",ipv6_count=" + strconv.Itoa(len(p.IPv6)) + "}"
}
