package bootstrap

import (
	"errors"
	"net/http"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
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
