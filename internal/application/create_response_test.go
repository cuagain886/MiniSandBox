package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// TestCreateAcceptedPersistsAndReturnsOneExpiry 验证有 key 与无 key 分支复用同一租约 builder。
func TestCreateAcceptedPersistsAndReturnsOneExpiry(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		name := "without key"
		if keyed {
			name = "with key"
		}
		t.Run(name, func(t *testing.T) {
			storeFake, idGenerator, clock, builder, waker := newSandboxServiceTestDependencies()
			service := NewSandboxServiceWithCreatePolicy(
				storeFake, idGenerator, clock, builder, waker, false,
				CreatePolicy{DefaultTTL: 30 * time.Minute, MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour, MaxSandboxes: 100},
			)
			command := CreateSandbox{Image: "alpine:3.22"}
			if keyed {
				key, err := NewLocalIdempotencyKey("create-response-key")
				if err != nil {
					t.Fatalf("key: %v", err)
				}
				command.Idempotency = &key
			}

			expiresAt := clock.now.UTC().Add(30 * time.Minute)
			spec, err := builder.Build(command)
			if err != nil {
				t.Fatalf("build spec: %v", err)
			}
			expected := domain.Sandbox{
				ID: idGenerator.id, Spec: spec, DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending,
				Reason: createAcceptedReason, Message: createAcceptedMessage, SpecHash: spec.Hash(),
				CreatedAt: clock.now.UTC(), UpdatedAt: clock.now.UTC(), LastTransitionAt: clock.now.UTC(),
				ExpiresAt: &expiresAt, Origin: domain.SandboxOriginAPI,
			}
			response, err := createAcceptedResponse(expected)
			if err != nil {
				t.Fatalf("expected response: %v", err)
			}
			if keyed {
				storeFake.SetCreateIdempotentResult(storeport.IdempotentCreateResult{Sandbox: expected, Response: response}, nil)
			} else {
				storeFake.SetCreateNonIdempotentResult(expected, nil)
			}

			outcome, err := service.CreateAccepted(context.Background(), command)
			if err != nil {
				t.Fatalf("create accepted: %v", err)
			}
			var persisted domain.Sandbox
			if keyed {
				calls := storeFake.CreateIdempotentCalls()
				if len(calls) != 1 || string(calls[0].Response.Body) != string(outcome.Body) {
					t.Fatalf("keyed calls/outcome: %#v/%#v", calls, outcome)
				}
				persisted = calls[0].Sandbox
			} else {
				calls := storeFake.CreateNonIdempotentCalls()
				if len(calls) != 1 || string(response.Body) != string(outcome.Body) {
					t.Fatalf("non-keyed calls/outcome: %#v/%#v", calls, outcome)
				}
				persisted = calls[0].Sandbox
			}
			var decoded protocol.Sandbox
			if err := json.Unmarshal(outcome.Body, &decoded); err != nil {
				t.Fatalf("decode outcome: %v", err)
			}
			if persisted.ExpiresAt == nil || !persisted.ExpiresAt.Equal(expiresAt) || !decoded.ExpiresAt.Equal(expiresAt) || clock.calls != 1 {
				t.Fatalf("expiry mismatch: persisted=%v response=%v clock=%d", persisted.ExpiresAt, decoded.ExpiresAt, clock.calls)
			}
		})
	}
}

// TestCreateAcceptedResponseUsesExactUTCExpiry 验证首次响应保存与 Store 相同的绝对租约。
func TestCreateAcceptedResponseUsesExactUTCExpiry(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	createdAt := time.Date(2028, 2, 3, 4, 5, 6, 789, location)
	expiresAt := createdAt.Add(30 * time.Minute)
	sandbox := domain.Sandbox{
		ID: "create-response", Spec: domain.SandboxSpec{Image: "alpine:3.22"},
		Message: createAcceptedMessage, CreatedAt: createdAt, UpdatedAt: createdAt,
		ExpiresAt: &expiresAt,
	}
	response, err := createAcceptedResponse(sandbox)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	var decoded protocol.Sandbox
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if !decoded.ExpiresAt.Equal(expiresAt) || decoded.ExpiresAt.Location() != time.UTC ||
		!response.CreatedAt.Equal(createdAt) || response.CreatedAt.Location() != time.UTC {
		t.Fatalf("response expiry/timestamps: response=%#v decoded=%#v", response, decoded)
	}
	for _, fragment := range []string{
		`"expires_at":"2028-02-02T20:35:06.000000789Z"`,
		`"created_at":"2028-02-02T20:05:06.000000789Z"`,
	} {
		if !strings.Contains(string(response.Body), fragment) {
			t.Fatalf("response is not UTC JSON: %s", response.Body)
		}
	}
}

// TestCreateAcceptedResponseRejectsMissingExpiry 验证事务输入不会保存缺失租约的响应。
func TestCreateAcceptedResponseRejectsMissingExpiry(t *testing.T) {
	_, err := createAcceptedResponse(domain.Sandbox{ID: "missing"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing expiry: got %v, want ErrInvalid", err)
	}
}
