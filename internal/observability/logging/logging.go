// Package logging 定义 MiniSandbox 结构化日志的固定字段、安全值和受限写入端口。
//
// 本包负责阻止 raw error、用户字符串和任意 attribute 进入日志；它不生成业务 ID、
// 不决定生命周期结果，也不替代 metrics 或 tracing。
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
)

// Field 是全项目唯一允许使用的结构化日志字段名。
type Field string

const (
	// FieldComponent 标识产生日志的稳定模块。
	FieldComponent Field = "component"
	// FieldRequestID 标识单次 HTTP 请求。
	FieldRequestID Field = "request_id"
	// FieldSandboxID 标识单个 sandbox。
	FieldSandboxID Field = "sandbox_id"
	// FieldExecutionID 标识单次 execution。
	FieldExecutionID Field = "execution_id"
	// FieldOperation 标识固定业务操作。
	FieldOperation Field = "operation"
	// FieldAttempt 标识从一开始的重试次数。
	FieldAttempt Field = "attempt"
	// FieldDurationMS 标识操作耗时，单位为毫秒。
	FieldDurationMS Field = "duration_ms"
	// FieldDelayMS 标识计划等待时间，单位为毫秒。
	FieldDelayMS Field = "delay_ms"
	// FieldResult 标识固定操作结果。
	FieldResult Field = "result"
	// FieldErrorCode 标识不含错误文本的机器码。
	FieldErrorCode Field = "error_code"
	// FieldErrorClass 标识固定错误类别。
	FieldErrorClass Field = "error_class"
	// FieldIdempotencyPresent 只表示请求是否携带幂等键，不记录键值。
	FieldIdempotencyPresent Field = "idempotency_present"
	// FieldIdempotencyOutcome 标识 absent、accepted、replay 或 conflict。
	FieldIdempotencyOutcome Field = "idempotency_outcome"
	// FieldImageHash 标识 image 引用的带域 SHA-256 摘要。
	FieldImageHash Field = "image_hash"
	// FieldFieldCount 标识规范化请求字段数量。
	FieldFieldCount Field = "field_count"
	// FieldClassification 标识固定 anomaly 分类。
	FieldClassification Field = "classification"
	// FieldFingerprintPrefix 标识安全指纹的短前缀，不记录完整 runtime 数据。
	FieldFingerprintPrefix Field = "fingerprint_prefix"
)

// IDKind 区分 safe ID 的用途，避免把 request ID 误记为 sandbox ID。
type IDKind string

const (
	// IDKindRequest 表示 HTTP request ID。
	IDKindRequest IDKind = "request"
	// IDKindSandbox 表示 sandbox ID。
	IDKindSandbox IDKind = "sandbox"
	// IDKindExecution 表示 execution ID。
	IDKindExecution IDKind = "execution"
)

// SafeID 是校验后才能构造的日志 ID；零值表示省略字段。
type SafeID struct {
	kind  IDKind
	value string
}

// NewSafeID 校验长度和 ASCII allowlist 后构造 typed ID。
func NewSafeID(kind IDKind, value string) (SafeID, error) {
	maximum := 128
	if kind == IDKindSandbox {
		maximum = 64
	}
	if kind != IDKindRequest && kind != IDKindSandbox && kind != IDKindExecution ||
		len(value) < 1 || len(value) > maximum || !safeToken(value) {
		return SafeID{}, fmt.Errorf("invalid safe %s ID", kind)
	}
	return SafeID{kind: kind, value: value}, nil
}

// String 返回已经验证的 ID；仅供协议回传和日志字段构造。
func (id SafeID) String() string { return id.value }

// Kind 返回 ID 的固定用途。
func (id SafeID) Kind() IDKind { return id.kind }

// LogValuer 把 ID 输出为字符串值；零值输出空值且通常应由调用方省略。
func (id SafeID) LogValue() slog.Value { return slog.StringValue(id.value) }

// SafeValue 是 operation、result、component 和 error 分类使用的受限字符串。
type SafeValue struct{ value string }

// NewSafeValue 只接受有限长度的 ASCII 机器值，拒绝空白、控制字符和用户文本。
func NewSafeValue(value string) (SafeValue, error) {
	if len(value) < 1 || len(value) > 64 || !safeToken(value) {
		return SafeValue{}, fmt.Errorf("invalid safe log value")
	}
	return SafeValue{value: value}, nil
}

// String 返回已验证的机器值。
func (value SafeValue) String() string { return value.value }

// Attr 是只能由本包构造的安全 slog attribute。
type Attr struct{ value slog.Attr }

// IDAttr 为匹配 IDKind 的固定字段生成 attribute；kind 不匹配时返回错误。
func IDAttr(field Field, id SafeID) (Attr, error) {
	want := map[Field]IDKind{FieldRequestID: IDKindRequest, FieldSandboxID: IDKindSandbox, FieldExecutionID: IDKindExecution}[field]
	if want == "" || id.kind != want || id.value == "" {
		return Attr{}, fmt.Errorf("invalid ID log attribute")
	}
	return Attr{value: slog.Any(string(field), id)}, nil
}

// ValueAttr 为固定字符串字段生成 attribute。
func ValueAttr(field Field, value SafeValue) (Attr, error) {
	switch field {
	case FieldComponent, FieldOperation, FieldResult, FieldErrorCode, FieldErrorClass,
		FieldIdempotencyOutcome, FieldClassification, FieldFingerprintPrefix, FieldImageHash:
	default:
		return Attr{}, fmt.Errorf("invalid safe value log field")
	}
	if value.value == "" {
		return Attr{}, fmt.Errorf("empty safe log value")
	}
	return Attr{value: slog.String(string(field), value.value)}, nil
}

// UintAttr 为 attempt 或字段计数生成非负整数 attribute。
func UintAttr(field Field, value uint64) (Attr, error) {
	if field != FieldAttempt && field != FieldFieldCount {
		return Attr{}, fmt.Errorf("invalid unsigned log field")
	}
	return Attr{value: slog.Uint64(string(field), value)}, nil
}

// DurationAttr 把非负 duration 转为固定毫秒字段。
func DurationAttr(field Field, value time.Duration) (Attr, error) {
	if field != FieldDurationMS && field != FieldDelayMS || value < 0 {
		return Attr{}, fmt.Errorf("invalid duration log attribute")
	}
	return Attr{value: slog.Int64(string(field), value.Milliseconds())}, nil
}

// BoolAttr 只允许记录幂等键 presence，不能记录键值。
func BoolAttr(field Field, value bool) (Attr, error) {
	if field != FieldIdempotencyPresent {
		return Attr{}, fmt.Errorf("invalid boolean log field")
	}
	return Attr{value: slog.Bool(string(field), value)}, nil
}

// Logger 是拒绝 raw slog.Attr 和 raw error 的结构化日志端口。
type Logger struct{ logger *slog.Logger }

// New 创建使用显式 slog.Logger 的安全日志端口。
func New(logger *slog.Logger) (*Logger, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger must not be nil")
	}
	return &Logger{logger: slog.New(utcHandler{next: logger.Handler()})}, nil
}

// Log 写入固定消息和安全 attributes；时间、level 由 slog handler 统一生成。
func (l *Logger) Log(ctx context.Context, level slog.Level, message SafeValue, attrs ...Attr) {
	if l == nil || l.logger == nil || message.value == "" {
		return
	}
	values := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		if attr.value.Key != "" {
			values = append(values, attr.value)
		}
	}
	l.logger.Log(ctx, level, message.value, values...)
}

func safeToken(value string) bool {
	if strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}

type utcHandler struct{ next slog.Handler }

func (handler utcHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler utcHandler) Handle(ctx context.Context, record slog.Record) error {
	record.Time = record.Time.UTC()
	return handler.next.Handle(ctx, record)
}

func (handler utcHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return utcHandler{next: handler.next.WithAttrs(attrs)}
}

func (handler utcHandler) WithGroup(name string) slog.Handler {
	return utcHandler{next: handler.next.WithGroup(name)}
}
