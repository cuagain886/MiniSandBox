package egressanchor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
)

// MaxAttestationBytes 是 readiness attestation regular file 的大小上限。
const MaxAttestationBytes = 4096

// Attestation 是 sidecar Ready 后由 adapter 只读取得的封闭 JSON 模型。
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
	// CreatedAt 是 attestation 原子发布前的 UTC 时间。
	CreatedAt time.Time `json:"created_at"`
}

// WriteAttestation 以 0400 regular file 原子发布有界 JSON；已存在目标不会被覆盖，
// 防止同一 sidecar 进程重复声明 Ready。
func WriteAttestation(path string, attestation Attestation) error {
	if err := validateAttestation(attestation); err != nil {
		return err
	}
	encoded, err := json.Marshal(attestation)
	if err != nil || len(encoded) == 0 || len(encoded) > MaxAttestationBytes {
		return errors.New("encode egress attestation")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("create egress attestation directory")
	}
	temporaryPath := path + ".tmp"
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return errors.New("create egress attestation temporary file")
	}
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(encoded); err != nil {
		return errors.New("write egress attestation")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync egress attestation")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close egress attestation")
	}
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return errors.New("egress attestation already exists")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("publish egress attestation")
	}
	removeTemporary = false
	return nil
}

// ReadAttestation 只接受不超过 4096 bytes、只读、字段封闭且版本有效的 regular file。
func ReadAttestation(path string) (Attestation, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxAttestationBytes || info.Mode().Perm()&0o222 != 0 {
		return Attestation{}, errors.New("egress attestation file is invalid")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Attestation{}, errors.New("read egress attestation")
	}
	return ParseAttestation(content)
}

// ParseAttestation 验证 adapter 从 Docker archive 只读取得的有界 attestation JSON。
func ParseAttestation(content []byte) (Attestation, error) {
	if len(content) == 0 || len(content) > MaxAttestationBytes {
		return Attestation{}, errors.New("egress attestation content size is invalid")
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
