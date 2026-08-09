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
	"strings"

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
	NetworkNamespace  string   `json:"network_namespace"`
	ImageDigest       string   `json:"image_digest"`
	AnchorUID         uint32   `json:"anchor_uid"`
	AnchorGID         uint32   `json:"anchor_gid"`
}

// Bootstrap 是经过严格 framing 与字段校验、可交给 sidecar 启动流程的可信配置。
type Bootstrap struct {
	// Policy 是控制面生成且 hash 已回验的不可变拒绝策略。
	Policy egresspolicy.Policy
	// NetworkNamespace 是控制面从 sidecar init PID 观察到的预期 netns identity。
	NetworkNamespace string
	// ImageDigest 是当前 sidecar 的精确 OCI sha256 digest reference。
	ImageDigest string
	// AnchorUID 是 Ready 后必须保持的非 root 有效 UID。
	AnchorUID uint32
	// AnchorGID 是 Ready 后必须保持的非 root 有效 GID。
	AnchorGID uint32
}

// ReadBootstrap 严格读取一帧 uint32 big-endian 长度、JSON payload 和 EOF。
// 空帧、超限、未知字段、尾随 JSON/字节及非规范策略全部 fail closed。
func ReadBootstrap(reader io.Reader) (Bootstrap, error) {
	buffered := bufio.NewReader(reader)
	var length uint32
	if err := binary.Read(buffered, binary.BigEndian, &length); err != nil {
		return Bootstrap{}, errors.New("read egress bootstrap length")
	}
	if length == 0 || length > MaxBootstrapBytes {
		return Bootstrap{}, errors.New("egress bootstrap size is invalid")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(buffered, payload); err != nil {
		return Bootstrap{}, errors.New("read egress bootstrap payload")
	}
	if _, err := buffered.ReadByte(); !errors.Is(err, io.EOF) {
		return Bootstrap{}, errors.New("egress bootstrap has trailing data")
	}
	if err := rejectDuplicateJSONFields(payload); err != nil {
		return Bootstrap{}, err
	}

	decoder := json.NewDecoder(newByteReader(payload))
	decoder.DisallowUnknownFields()
	var wire bootstrapWire
	if err := decoder.Decode(&wire); err != nil {
		return Bootstrap{}, errors.New("decode egress bootstrap")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Bootstrap{}, err
	}
	policy, err := wire.policy()
	if err != nil {
		return Bootstrap{}, err
	}
	if err := egresspolicy.Verify(policy); err != nil {
		return Bootstrap{}, errors.New("egress bootstrap policy is invalid")
	}
	if !ValidNetworkNamespace(wire.NetworkNamespace) || !ValidImageDigest(wire.ImageDigest) || wire.AnchorUID == 0 || wire.AnchorGID == 0 {
		return Bootstrap{}, errors.New("egress bootstrap identity is invalid")
	}
	return Bootstrap{
		Policy: policy, NetworkNamespace: wire.NetworkNamespace, ImageDigest: wire.ImageDigest,
		AnchorUID: wire.AnchorUID, AnchorGID: wire.AnchorGID,
	}, nil
}

func rejectDuplicateJSONFields(payload []byte) error {
	decoder := json.NewDecoder(newByteReader(payload))
	if err := scanJSONValue(decoder); err != nil {
		return errors.New("egress bootstrap JSON is invalid")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object field")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

// ValidNetworkNamespace 验证公开 attestation 与内部 bootstrap 共用的 netns 身份格式。
func ValidNetworkNamespace(value string) bool {
	const prefix = "linux-netns:"
	if len(value) <= len(prefix) || len(value) > 96 || value[:len(prefix)] != prefix {
		return false
	}
	parts := strings.Split(value[len(prefix):], ":")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

// ValidImageDigest 验证 sidecar artifact 使用精确小写 sha256 digest reference。
func ValidImageDigest(value string) bool {
	separator := strings.LastIndex(value, "@sha256:")
	if separator < 1 || separator+len("@sha256:")+64 != len(value) {
		return false
	}
	for _, character := range value[separator+len("@sha256:"):] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
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
