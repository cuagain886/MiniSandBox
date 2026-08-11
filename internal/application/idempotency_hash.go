package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"minisandbox/internal/domain"
)

const (
	createHashDomain        = "minisandbox:create:v1"
	maxCanonicalCreateBytes = 16 << 10
)

// HashCanonicalCreateRequest 对已规范化的创建请求计算域分离 SHA-256。
//
// 返回值固定为 64 字节 lowercase hex；函数不接触 idempotency key、请求 ID、
// Authorization、时钟或绝对 expires，也不会把 canonical bytes 写入错误。
func HashCanonicalCreateRequest(canonical []byte) (string, error) {
	if len(canonical) == 0 || len(canonical) > maxCanonicalCreateBytes {
		return "", fmt.Errorf("hash canonical create request: %w", domain.ErrInvalid)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(createHashDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}
