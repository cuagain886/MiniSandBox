package egressanchor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

// MaxAttestationBytes 是 attach 控制响应中 attestation envelope 的大小上限。
const MaxAttestationBytes = 4096

// Attestation 是 sidecar Ready 后保存在 egressd 内存并通过 attach 返回的封闭模型。
type Attestation struct {
	// ProtocolVersion 是 egress bootstrap 精确协议版本。
	ProtocolVersion int `json:"protocol_version"`
	// RuleSchemaVersion 是 nft 规则精确 schema 版本。
	RuleSchemaVersion int `json:"rule_schema_version"`
	// PolicyHash 是规范化 deny policy 的 SHA-256 身份。
	PolicyHash string `json:"policy_hash"`
	// NetworkNamespace 是当前 anchor 从 /proc/self/ns/net 得到的身份。
	NetworkNamespace string `json:"network_namespace"`
	// ImageDigest 是 sidecar artifact 的精确 OCI digest reference。
	ImageDigest string `json:"image_digest"`
	// CreatedAt 是初次 nft 回验及永久降权完成后的 UTC 时间；inspect 不会改写它。
	CreatedAt time.Time `json:"created_at"`
}

// ParseAttestation 验证 attach 控制协议取得的有界、字段封闭 attestation JSON。
func ParseAttestation(content []byte) (Attestation, error) {
	if len(content) == 0 || len(content) > MaxAttestationBytes {
		return Attestation{}, errors.New("egress attestation content size is invalid")
	}
	if err := rejectDuplicateAttestationFields(content); err != nil {
		return Attestation{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var attestation Attestation
	if err := decoder.Decode(&attestation); err != nil {
		return Attestation{}, errors.New("decode egress attestation")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Attestation{}, errors.New("egress attestation has trailing data")
	}
	if err := validateAttestation(attestation); err != nil {
		return Attestation{}, err
	}
	return attestation, nil
}

func rejectDuplicateAttestationFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("egress attestation JSON is invalid")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errors.New("egress attestation JSON is invalid")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("egress attestation JSON is invalid")
		}
		if _, exists := seen[key]; exists {
			return errors.New("egress attestation field is duplicated")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("egress attestation JSON is invalid")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return errors.New("egress attestation JSON is invalid")
	}
	return nil
}

func validateAttestation(attestation Attestation) error {
	if attestation.ProtocolVersion != egresspolicy.CurrentProtocolVersion || attestation.RuleSchemaVersion != egresspolicy.CurrentRuleSchemaVersion {
		return errors.New("egress attestation version is invalid")
	}
	if len(attestation.PolicyHash) != 64 || !lowerHex(attestation.PolicyHash) ||
		!egressnft.ValidNetworkNamespace(attestation.NetworkNamespace) || !egressnft.ValidImageDigest(attestation.ImageDigest) ||
		attestation.CreatedAt.IsZero() || attestation.CreatedAt.Location() != time.UTC {
		return errors.New("egress attestation content is invalid")
	}
	return nil
}

func lowerHex(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
