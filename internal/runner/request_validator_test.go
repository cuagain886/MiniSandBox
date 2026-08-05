package runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

func testRequestValidator(t *testing.T) *RequestValidator {
	t.Helper()
	validator, err := NewRequestValidator(runnerbootstrap.Limits{
		DefaultTimeoutNanoseconds: 10 * time.Second,
		MaxTimeoutNanoseconds:     time.Minute,
		MaxRequestBytes:           16,
	})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	return validator
}

// TestRequestValidatorAcceptsArgvAndShellModes 验证两种命令模式、timeout 边界和 background 透传。
func TestRequestValidatorAcceptsArgvAndShellModes(t *testing.T) {
	validator := testRequestValidator(t)
	tests := []struct {
		name    string
		request protocol.ExecuteRequest
		timeout time.Duration
	}{
		{name: "argv default timeout", request: protocol.ExecuteRequest{Argv: []string{"printf", "%s", "a b"}}, timeout: 10 * time.Second},
		{name: "shell maximum timeout", request: protocol.ExecuteRequest{Shell: "printf ok", TimeoutSeconds: 60, Background: true}, timeout: time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validator.Validate(test.request)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got.Timeout != test.timeout || got.Background != test.request.Background || got.Shell != test.request.Shell {
				t.Fatalf("validated request: %+v", got)
			}
			if len(test.request.Argv) > 0 {
				got.Argv[0] = "changed"
				if test.request.Argv[0] != "printf" {
					t.Fatal("validator mutated caller argv")
				}
			}
		})
	}
}

// TestRequestValidatorRejectsInvalidCommandMatrix 覆盖互斥、空命令、NUL 和命令字节上限。
func TestRequestValidatorRejectsInvalidCommandMatrix(t *testing.T) {
	validator := testRequestValidator(t)
	tests := []protocol.ExecuteRequest{
		{},
		{Argv: []string{}},
		{Argv: []string{"echo"}, Shell: "echo"},
		{Argv: []string{""}},
		{Argv: []string{"echo\x00bad"}},
		{Argv: []string{"echo", "bad\x00value"}},
		{Argv: []string{"123456789", "12345678"}},
		{Shell: "   \t\n"},
		{Shell: "echo\x00bad"},
		{Shell: strings.Repeat("x", 17)},
	}
	for index, request := range tests {
		if _, err := validator.Validate(request); !errors.Is(err, ErrInvalidExecutionRequest) {
			t.Fatalf("case %d: got %v, want invalid request", index, err)
		}
	}
}

// TestRequestValidatorRejectsArgumentCountOverflow 验证参数条目数存在独立硬上限。
func TestRequestValidatorRejectsArgumentCountOverflow(t *testing.T) {
	validator, err := NewRequestValidator(runnerbootstrap.Limits{
		DefaultTimeoutNanoseconds: time.Second,
		MaxTimeoutNanoseconds:     time.Minute,
		MaxRequestBytes:           maxExecutionArguments + 1,
	})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	argv := make([]string, maxExecutionArguments+1)
	for index := range argv {
		argv[index] = "x"
	}
	if _, err := validator.Validate(protocol.ExecuteRequest{Argv: argv}); !errors.Is(err, ErrInvalidExecutionRequest) {
		t.Fatalf("argument count overflow: %v", err)
	}
}

// TestRequestValidatorRejectsInvalidTimeouts 验证负数、超过最大值和乘法溢出均被拒绝。
func TestRequestValidatorRejectsInvalidTimeouts(t *testing.T) {
	validator := testRequestValidator(t)
	for _, seconds := range []int64{-1, 61, 1<<63 - 1} {
		if _, err := validator.Validate(protocol.ExecuteRequest{Argv: []string{"ok"}, TimeoutSeconds: seconds}); !errors.Is(err, ErrInvalidExecutionRequest) {
			t.Fatalf("timeout %d: %v", seconds, err)
		}
	}
}

// TestNewRequestValidatorRejectsInvalidLimits 验证不可信或无界 bootstrap limit 不能创建 validator。
func TestNewRequestValidatorRejectsInvalidLimits(t *testing.T) {
	for _, limits := range []runnerbootstrap.Limits{
		{},
		{DefaultTimeoutNanoseconds: time.Minute, MaxTimeoutNanoseconds: time.Second, MaxRequestBytes: 1},
		{DefaultTimeoutNanoseconds: time.Second, MaxTimeoutNanoseconds: time.Minute},
	} {
		if _, err := NewRequestValidator(limits); err == nil {
			t.Fatalf("invalid limits accepted: %+v", limits)
		}
	}
}
