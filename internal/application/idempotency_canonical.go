package application

import (
	"encoding/json"
	"fmt"

	"github.com/distribution/reference"

	"minisandbox/internal/domain"
)

const createContractVersion = "lifecycle.create.v1"

// CanonicalCreateRequest 是创建幂等语义的显式输入，不包含 transport 或服务端运行结果。
//
// TTLSeconds 的 nil 与非 nil 必须保留区别；Network.Outbound 的缺失由 API contract
// 明确定义为 false，因此输入只保留规范化后的布尔语义。
type CanonicalCreateRequest struct {
	// Image 是客户端请求的 image reference。
	Image string
	// TTLSeconds 是客户端显式提供的整秒 TTL；nil 表示字段缺失。
	TTLSeconds *int64
	// Outbound 是规范化后的公网出站意图。
	Outbound bool
}

// canonicalCreateDocument 是字段顺序固定且与 runtime/domain 对象解耦的 wire document。
type canonicalCreateDocument struct {
	ContractVersion string               `json:"contract_version"`
	Image           string               `json:"image"`
	TTLSeconds      canonicalOptionalTTL `json:"ttl_seconds"`
	Network         canonicalNetwork     `json:"network"`
}

// canonicalOptionalTTL 用显式 presence 防止服务端默认值变化改写旧请求身份。
type canonicalOptionalTTL struct {
	Presence string `json:"presence"`
	Value    *int64 `json:"value,omitempty"`
}

// canonicalNetwork 保存 contract 已规范化的唯一网络选项。
type canonicalNetwork struct {
	Outbound bool `json:"outbound"`
}

// CanonicalizeCreateRequest 返回稳定、紧凑且字段顺序固定的 JSON bytes。
//
// 本函数不读取时钟、不填入默认 TTL、不计算 hash，也不接受 map 或任意扩展字段，
// 因而未知请求字段不可能静默进入幂等身份。
func CanonicalizeCreateRequest(request CanonicalCreateRequest) ([]byte, error) {
	image, err := normalizeCanonicalImage(request.Image)
	if err != nil {
		return nil, err
	}
	ttl := canonicalOptionalTTL{Presence: "absent"}
	if request.TTLSeconds != nil {
		value := *request.TTLSeconds
		ttl = canonicalOptionalTTL{Presence: "present", Value: &value}
	}
	document := canonicalCreateDocument{
		ContractVersion: createContractVersion,
		Image:           image,
		TTLSeconds:      ttl,
		Network:         canonicalNetwork{Outbound: request.Outbound},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode canonical create request: %w", err)
	}
	return encoded, nil
}

// normalizeCanonicalImage 使用 OCI/Docker reference 规则生成完整稳定名称。
func normalizeCanonicalImage(value string) (string, error) {
	if value == "" || len(value) > domain.MaxImageReferenceLength {
		return "", fmt.Errorf("canonicalize create image: %w", domain.ErrInvalid)
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize create image: %w", domain.ErrInvalid)
	}
	return named.String(), nil
}
