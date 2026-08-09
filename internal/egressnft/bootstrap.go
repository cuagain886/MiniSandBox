// Package egressnft 实现 egress sidecar 的有界 bootstrap framing、静态 nftables
// 规则编译、原子安装与只读回验。
//
// 本包只接受控制面生成的规范化 Policy，不接收 sandbox 请求或任意 nft 文本；它不
// 创建 Docker 资源、不处理 DNS/FQDN，也不提供运行时策略更新接口。
package egressnft

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/netip"

	"minisandbox/internal/egresspolicy"
)

const (
	// MaxBootstrapBytes 是 bootstrap JSON 帧允许的最大字节数。
	MaxBootstrapBytes = 65536
	// TableName 是 sidecar 独占的 nftables inet table 名称。
	TableName = "minisandbox_egress"
)

type bootstrapWire struct {
	ProtocolVersion   int      `json:"protocol_version"`
	RuleSchemaVersion int      `json:"rule_schema_version"`
	PolicyHash        string   `json:"policy_hash"`
	IPv4Denied        []string `json:"ipv4_denied"`
	IPv6Denied        []string `json:"ipv6_denied"`
}

// ReadBootstrap 严格读取一帧 uint32 big-endian 长度、JSON payload 和 EOF。
// 空帧、超限、未知字段、尾随 JSON/字节及非规范策略全部 fail closed。
func ReadBootstrap(reader io.Reader) (egresspolicy.Policy, error) {
	buffered := bufio.NewReader(reader)
	var length uint32
	if err := binary.Read(buffered, binary.BigEndian, &length); err != nil {
		return egresspolicy.Policy{}, errors.New("read egress bootstrap length")
	}
	if length == 0 || length > MaxBootstrapBytes {
		return egresspolicy.Policy{}, errors.New("egress bootstrap size is invalid")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(buffered, payload); err != nil {
		return egresspolicy.Policy{}, errors.New("read egress bootstrap payload")
	}
	if _, err := buffered.ReadByte(); !errors.Is(err, io.EOF) {
		return egresspolicy.Policy{}, errors.New("egress bootstrap has trailing data")
	}

	decoder := json.NewDecoder(newByteReader(payload))
	decoder.DisallowUnknownFields()
	var wire bootstrapWire
	if err := decoder.Decode(&wire); err != nil {
		return egresspolicy.Policy{}, errors.New("decode egress bootstrap")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return egresspolicy.Policy{}, err
	}
	policy, err := wire.policy()
	if err != nil {
		return egresspolicy.Policy{}, err
	}
	if err := egresspolicy.Verify(policy); err != nil {
		return egresspolicy.Policy{}, errors.New("egress bootstrap policy is invalid")
	}
	return policy, nil
}

func (wire bootstrapWire) policy() (egresspolicy.Policy, error) {
	ipv4, err := parsePrefixes(wire.IPv4Denied, true)
	if err != nil {
		return egresspolicy.Policy{}, err
	}
	ipv6, err := parsePrefixes(wire.IPv6Denied, false)
	if err != nil {
		return egresspolicy.Policy{}, err
	}
	return egresspolicy.Policy{
		ProtocolVersion:   wire.ProtocolVersion,
		RuleSchemaVersion: wire.RuleSchemaVersion,
		IPv4:              ipv4,
		IPv6:              ipv6,
		Hash:              wire.PolicyHash,
	}, nil
}

func parsePrefixes(values []string, ipv4 bool) ([]netip.Prefix, error) {
	if values == nil {
		return nil, errors.New("egress bootstrap deny set is missing")
	}
	result := make([]netip.Prefix, len(values))
	for index, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() || prefix.Addr().Is4() != ipv4 || prefix != prefix.Masked() {
			return nil, errors.New("egress bootstrap deny prefix is invalid")
		}
		result[index] = prefix
	}
	return result, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("egress bootstrap JSON has trailing value")
	}
	return nil
}

// byteReader 隔离 bytes.Reader 的构造，保持 framing 路径不接触字符串转换。
type byteReader struct {
	data   []byte
	offset int
}

func newByteReader(data []byte) *byteReader { return &byteReader{data: data} }

func (reader *byteReader) Read(target []byte) (int, error) {
	if reader.offset == len(reader.data) {
		return 0, io.EOF
	}
	count := copy(target, reader.data[reader.offset:])
	reader.offset += count
	return count, nil
}
