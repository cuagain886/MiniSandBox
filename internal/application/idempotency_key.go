package application

import (
	"fmt"
	"log/slog"

	"minisandbox/internal/domain"
)

const localIdempotencyScope = "local:v1"

// IdempotencyKey 是通过校验的 scope/key 值对象。
//
// 字段保持私有且 String/LogValue 只暴露 present=true，避免结构化日志或通用
// fmt 意外记录 raw key；Store 调用方必须通过显式 accessor 读取值。
type IdempotencyKey struct {
	scopeID string
	key     string
}

// NewLocalIdempotencyKey 为当前单租户 local:v1 scope 创建受限 key。
func NewLocalIdempotencyKey(value string) (IdempotencyKey, error) {
	return newIdempotencyKey(localIdempotencyScope, value)
}

// ScopeID 返回持久化复合键使用的稳定 scope 标识。
func (k IdempotencyKey) ScopeID() string {
	return k.scopeID
}

// Value 返回已校验的 raw key，只能传给幂等 Store 端口，不得用于日志或响应。
func (k IdempotencyKey) Value() string {
	return k.key
}

// String 返回安全存在性摘要，禁止 fmt 默认展开私有 raw key。
func (k IdempotencyKey) String() string {
	return "idempotency_key_present=true"
}

// LogValue 让 slog 只记录低基数存在性，不记录 key 或可关联 hash。
func (k IdempotencyKey) LogValue() slog.Value {
	return slog.BoolValue(true)
}

// newIdempotencyKey 校验 scope 和 key 的共享 ASCII/长度约束。
func newIdempotencyKey(scopeID, value string) (IdempotencyKey, error) {
	if !validScopedToken(scopeID, 64) || !validScopedToken(value, 128) {
		return IdempotencyKey{}, fmt.Errorf("map idempotency key: %w", domain.ErrInvalid)
	}
	return IdempotencyKey{scopeID: scopeID, key: value}, nil
}

// validScopedToken 与 SQLite CHECK 共享 1..limit 的安全 ASCII 字符集语义。
func validScopedToken(value string, limit int) bool {
	if len(value) < 1 || len(value) > limit {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}
