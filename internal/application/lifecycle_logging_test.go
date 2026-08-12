package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
)

type lifecycleLoggingFake struct {
	createOutcome IdempotentCreateOutcome
	createErr     error
	getErr        error
	deleteErr     error
	renewErr      error
}

func (f *lifecycleLoggingFake) CreateAccepted(context.Context, CreateSandbox) (IdempotentCreateOutcome, error) {
	return f.createOutcome, f.createErr
}
func (f *lifecycleLoggingFake) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, f.getErr
}
func (f *lifecycleLoggingFake) Delete(context.Context, DeleteSandbox) (domain.Sandbox, error) {
	return domain.Sandbox{}, f.deleteErr
}
func (f *lifecycleLoggingFake) Renew(context.Context, RenewSandbox) (domain.Sandbox, error) {
	return domain.Sandbox{}, f.renewErr
}

type lifecycleLoggingClock struct{ now time.Time }

func (clock *lifecycleLoggingClock) Now() time.Time {
	result := clock.now
	clock.now = clock.now.Add(5 * time.Millisecond)
	return result
}

// TestLifecycleLoggingCoversSuccessReplayConflictAndLimit 验证固定 lifecycle 与幂等结果分支。
func TestLifecycleLoggingCoversSuccessReplayConflictAndLimit(t *testing.T) {
	cases := []struct {
		name, wantOutcome, wantCode string
		fake                        lifecycleLoggingFake
		idempotent                  bool
	}{{"success", "absent", "", lifecycleLoggingFake{createOutcome: IdempotentCreateOutcome{SandboxID: "sandbox-1"}}, false},
		{"replay", "replay", "", lifecycleLoggingFake{createOutcome: IdempotentCreateOutcome{SandboxID: "sandbox-1", Replayed: true}}, true},
		{"conflict", "conflict", "IDEMPOTENCY_CONFLICT", lifecycleLoggingFake{createErr: domain.ErrIdempotencyConflict}, true},
		{"limit", "failed", "SANDBOX_LIMIT_REACHED", lifecycleLoggingFake{createErr: domain.ErrSandboxLimitReached}, true}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decorator, output := newLifecycleLoggingTestService(t, &testCase.fake)
			command := CreateSandbox{Image: "registry.example/secret-user:secret-pass@image"}
			if testCase.idempotent {
				key, err := NewLocalIdempotencyKey("super-secret-key")
				if err != nil {
					t.Fatal(err)
				}
				command.Idempotency = &key
			}
			_, _ = decorator.CreateAccepted(context.Background(), command)
			logs := decodeLogLines(t, output.String())
			if len(logs) != 2 || logs[1]["idempotency_outcome"] != testCase.wantOutcome ||
				(testCase.wantCode != "" && logs[1]["error_code"] != testCase.wantCode) {
				t.Fatalf("logs: %#v", logs)
			}
			if strings.Contains(output.String(), "secret-user") || strings.Contains(output.String(), "secret-key") {
				t.Fatalf("secret leaked: %s", output.String())
			}
		})
	}
}

// TestLifecycleLoggingNeverWritesRawErrorsOrDomainContent 验证 get/delete/renew 失败日志不包含错误、expiry 或领域 message。
func TestLifecycleLoggingNeverWritesRawErrorsOrDomainContent(t *testing.T) {
	fake := &lifecycleLoggingFake{getErr: errors.New("secret /host/path"), deleteErr: errors.New("secret delete"), renewErr: errors.New("2099-01-01 secret")}
	decorator, output := newLifecycleLoggingTestService(t, fake)
	ctx := context.Background()
	_, _ = decorator.Get(ctx, "sandbox-safe")
	_, _ = decorator.Delete(ctx, DeleteSandbox{SandboxID: "sandbox-safe"})
	_, _ = decorator.Renew(ctx, RenewSandbox{SandboxID: "sandbox-safe", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)})
	if logs := decodeLogLines(t, output.String()); len(logs) != 6 {
		t.Fatalf("log count: %d", len(logs))
	}
	for _, forbidden := range []string{"/host/path", "secret delete", "2099-01-01"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("forbidden value %q leaked: %s", forbidden, output.String())
		}
	}
}

func newLifecycleLoggingTestService(t *testing.T, next LifecycleOperations) (*LoggingSandboxService, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	logger, err := logging.New(slog.New(slog.NewJSONHandler(output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewLoggingSandboxService(next, logger, &lifecycleLoggingClock{now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return service, output
}

func decodeLogLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	result := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatal(err)
		}
		result = append(result, decoded)
	}
	return result
}
