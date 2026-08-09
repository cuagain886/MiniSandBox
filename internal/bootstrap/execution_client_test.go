package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestOutboundExecutionAdmissionBindsRunnerAndRuntimeIdentity 验证生产准入只把 runner
// 自报 netns 与当前 sandbox ID 交给受信 runtime，并在缺少身份能力或 runtime 拒绝时关闭准入。
func TestOutboundExecutionAdmissionBindsRunnerAndRuntimeIdentity(t *testing.T) {
	runtime := &executionEgressGateFake{}
	gate := outboundExecutionAdmission{runtime: runtime}
	client := executionIdentityClientFake{identity: "linux-netns:7:11"}
	if err := gate.Check(context.Background(), domain.Sandbox{ID: "sb_test"}, client); err != nil {
		t.Fatalf("check admission: %v", err)
	}
	if runtime.sandboxID != "sb_test" || runtime.identity != client.identity {
		t.Fatalf("runtime check got sandbox=%q identity=%q", runtime.sandboxID, runtime.identity)
	}

	if err := gate.Check(context.Background(), domain.Sandbox{ID: "sb_test"}, executionClientWithoutIdentity{}); err == nil {
		t.Fatal("client without network identity was admitted")
	}
	runtime.err = errors.New("sidecar drift")
	if err := gate.Check(context.Background(), domain.Sandbox{ID: "sb_test"}, client); err == nil {
		t.Fatal("runtime rejection was admitted")
	}
}

type executionIdentityClientFake struct {
	application.ExecutionClient
	identity string
	err      error
}

func (client executionIdentityClientFake) NetworkNamespace(context.Context) (string, error) {
	return client.identity, client.err
}

type executionClientWithoutIdentity struct {
	application.ExecutionClient
}

type executionEgressGateFake struct {
	sandboxID string
	identity  string
	err       error
}

func (gate *executionEgressGateFake) CheckSandboxEgress(_ context.Context, sandboxID, identity string) error {
	gate.sandboxID = sandboxID
	gate.identity = identity
	return gate.err
}

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
