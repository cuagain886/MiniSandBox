package application

import (
	"context"
	"errors"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/internal/testutil"
)

// TestCommitIdempotentCreatePreservesReplayResponse 验证 application 不重新映射当前状态。
func TestCommitIdempotentCreatePreservesReplayResponse(t *testing.T) {
	fake := testutil.NewFakeStore()
	body := []byte(`{"id":"first","state":"Pending"}`)
	fake.SetCreateIdempotentResult(storeport.IdempotentCreateResult{
		Sandbox:  domain.Sandbox{ID: "first", ObservedState: domain.StateTerminated},
		Response: storeport.IdempotentResponse{SchemaVersion: 1, StatusCode: 202, Location: "/v1/sandboxes/first", Body: body},
		Replayed: true,
	}, nil)
	service := NewSandboxService(fake, nil, nil, SandboxSpecBuilder{}, nil)
	outcome, err := service.CommitIdempotentCreate(context.Background(), storeport.IdempotentCreateRequest{})
	if err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	if outcome.SandboxID != "first" || outcome.StatusCode != 202 ||
		outcome.Location != "/v1/sandboxes/first" || string(outcome.Body) != string(body) || !outcome.Replayed {
		t.Fatalf("application replay outcome: %#v", outcome)
	}
	body[0] = '['
	if outcome.Body[0] != '{' {
		t.Fatal("application outcome reused Store body backing array")
	}
}

// TestCommitIdempotentCreatePreservesStoreError 验证 typed Store 错误可由后续 mapper 识别。
func TestCommitIdempotentCreatePreservesStoreError(t *testing.T) {
	fake := testutil.NewFakeStore()
	fake.SetCreateIdempotentResult(storeport.IdempotentCreateResult{}, domain.ErrIdempotencyConflict)
	service := NewSandboxService(fake, nil, nil, SandboxSpecBuilder{}, nil)
	_, err := service.CommitIdempotentCreate(context.Background(), storeport.IdempotentCreateRequest{})
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("got %v, want idempotency conflict", err)
	}
}

// TestCommitNonIdempotentCreateUsesSameResponseShape 验证无 key 分支不制造重放记录语义。
func TestCommitNonIdempotentCreateUsesSameResponseShape(t *testing.T) {
	fake := testutil.NewFakeStore()
	sandbox := domain.Sandbox{ID: "no-key"}
	fake.SetCreateNonIdempotentResult(sandbox, nil)
	response := storeport.IdempotentResponse{StatusCode: 202, Location: "/v1/sandboxes/no-key", Body: []byte(`{"id":"no-key"}`)}
	service := NewSandboxService(fake, nil, nil, SandboxSpecBuilder{}, nil)
	outcome, err := service.CommitNonIdempotentCreate(context.Background(), sandbox, response)
	if err != nil {
		t.Fatalf("commit no-key create: %v", err)
	}
	if outcome.Replayed || outcome.SandboxID != sandbox.ID || outcome.StatusCode != response.StatusCode ||
		outcome.Location != response.Location || string(outcome.Body) != string(response.Body) ||
		len(fake.CreateIdempotentCalls()) != 0 || len(fake.CreateNonIdempotentCalls()) != 1 {
		t.Fatalf("non-idempotent outcome: %#v calls=%#v", outcome, fake.CreateNonIdempotentCalls())
	}
}
