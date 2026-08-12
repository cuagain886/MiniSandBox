package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLoggerWritesStableJSONFields 验证 JSON 时间、level 与固定安全字段。
func TestLoggerWritesStableJSONFields(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	requestID, _ := NewSafeID(IDKindRequest, "req-123")
	requestAttr, _ := IDAttr(FieldRequestID, requestID)
	operation, _ := NewSafeValue("sandbox.create")
	operationAttr, _ := ValueAttr(FieldOperation, operation)
	durationAttr, _ := DurationAttr(FieldDurationMS, 1500*time.Microsecond)
	message, _ := NewSafeValue("operation.result")
	logger.Log(context.Background(), slog.LevelInfo, message, requestAttr, operationAttr, durationAttr)
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["level"] != "INFO" || decoded["request_id"] != "req-123" || decoded["operation"] != "sandbox.create" || decoded["duration_ms"] != float64(1) {
		t.Fatalf("decoded log: %#v", decoded)
	}
	parsed, err := time.Parse(time.RFC3339Nano, decoded["time"].(string))
	if err != nil || parsed.Location() != time.UTC {
		t.Fatalf("timestamp is not UTC: %v/%v", decoded["time"], err)
	}
}

// TestSafeTypesRejectRawErrorAndUserStrings 验证控制字符、空白和 raw error 文本不能成为安全值。
func TestSafeTypesRejectRawErrorAndUserStrings(t *testing.T) {
	unsafe := []string{"secret message with spaces", "line\nbreak", strings.Repeat("x", 129), "/host/private"}
	for _, value := range unsafe {
		if _, err := NewSafeValue(value); err == nil {
			t.Fatalf("unsafe value accepted: %q", value)
		}
		if _, err := NewSafeID(IDKindRequest, value); err == nil {
			t.Fatalf("unsafe ID accepted: %q", value)
		}
	}
	if _, err := IDAttr(FieldSandboxID, mustSafeID(t, IDKindRequest, "req-safe")); err == nil {
		t.Fatal("request ID accepted as sandbox ID")
	}
}

func mustSafeID(t *testing.T, kind IDKind, value string) SafeID {
	t.Helper()
	id, err := NewSafeID(kind, value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
