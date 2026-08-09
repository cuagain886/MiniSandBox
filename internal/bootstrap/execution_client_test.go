package bootstrap

import (
	"errors"
	"net/http"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestMapRunnerStatusErrorPreservesStableSemantics 验证 adapter 不把限流、未找到和请求错误降级为 runner unhealthy。
func TestMapRunnerStatusErrorPreservesStableSemantics(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{status: http.StatusBadRequest, want: domain.ErrInvalidExecutionRequest},
		{status: http.StatusUnprocessableEntity, want: domain.ErrInvalidExecutionRequest},
		{status: http.StatusNotFound, want: domain.ErrExecutionNotFound},
		{status: http.StatusTooManyRequests, want: domain.ErrExecutionLimitReached},
		{status: http.StatusInternalServerError, want: domain.ErrRunnerUnhealthy},
	}
	for _, test := range tests {
		got := mapRunnerStatusError(&runnerclient.StatusError{StatusCode: test.status})
		if !errors.Is(got, test.want) {
			t.Fatalf("status %d: got %v, want %v", test.status, got, test.want)
		}
	}
}

// TestMapRunnerStatusErrorUsesProtocolCode 验证同为 422 时仍保留 cwd 与 shell 的公共语义。
func TestMapRunnerStatusErrorUsesProtocolCode(t *testing.T) {
	for code, want := range map[string]error{
		string(protocol.ErrorCodeInvalidCWD):    domain.ErrInvalidCWD,
		string(protocol.ErrorCodeShellNotFound): domain.ErrShellNotFound,
	} {
		got := mapRunnerStatusError(&runnerclient.StatusError{StatusCode: http.StatusUnprocessableEntity, Code: code})
		if !errors.Is(got, want) {
			t.Fatalf("code %s: got %v, want %v", code, got, want)
		}
	}
}
