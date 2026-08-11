package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// CreateAccepted 执行 Phase 3 公共创建的规范化、原子提交和精确响应装配。
func (s *SandboxService) CreateAccepted(ctx context.Context, command CreateSandbox) (IdempotentCreateOutcome, error) {
	if command.Outbound && !s.allowOutbound {
		return IdempotentCreateOutcome{}, domain.ErrOutboundNotAllowed
	}
	if s.idGenerator == nil || s.clock == nil || s.store == nil ||
		s.createPolicy.DefaultTTL <= 0 || s.createPolicy.MinimumTTL <= 0 ||
		s.createPolicy.MaximumTTL < s.createPolicy.MinimumTTL || s.createPolicy.MaxSandboxes < 1 {
		return IdempotentCreateOutcome{}, fmt.Errorf("create service is not configured: %w", domain.ErrInvalid)
	}
	ttl, err := s.resolveCreateTTL(command.TTLSeconds)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	spec, err := s.specBuilder.Build(command)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	canonical, err := CanonicalizeCreateRequest(CanonicalCreateRequest{
		Image: command.Image, TTLSeconds: command.TTLSeconds, Outbound: command.Outbound,
	})
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	requestHash, err := HashCanonicalCreateRequest(canonical)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	id, err := s.idGenerator.NewID()
	if err != nil {
		return IdempotentCreateOutcome{}, fmt.Errorf("generate sandbox ID: %w", err)
	}
	now := s.clock.Now().UTC()
	expiresAt := now.Add(ttl)
	sandbox := domain.Sandbox{
		ID: id, Spec: spec, DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending,
		Reason: createAcceptedReason, Message: createAcceptedMessage, SpecHash: spec.Hash(),
		CreatedAt: now, UpdatedAt: now, LastTransitionAt: now, ExpiresAt: &expiresAt,
		Origin: domain.SandboxOriginAPI,
	}
	response, err := createAcceptedResponse(sandbox)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	if command.Idempotency == nil {
		return s.CommitNonIdempotentCreate(ctx, sandbox, response, s.createPolicy.MaxSandboxes)
	}
	return s.CommitIdempotentCreate(ctx, storeport.IdempotentCreateRequest{
		ScopeID: command.Idempotency.ScopeID(), Key: command.Idempotency.Value(), RequestHash: requestHash,
		Sandbox: sandbox, Response: response, MaxSandboxes: s.createPolicy.MaxSandboxes,
	})
}

func (s *SandboxService) resolveCreateTTL(seconds *int64) (time.Duration, error) {
	ttl := s.createPolicy.DefaultTTL
	if seconds != nil {
		if *seconds <= 0 || *seconds > int64(s.createPolicy.MaximumTTL/time.Second) {
			return 0, domain.ErrInvalidTTL
		}
		ttl = time.Duration(*seconds) * time.Second
	}
	if ttl < s.createPolicy.MinimumTTL || ttl > s.createPolicy.MaximumTTL || ttl%time.Second != 0 {
		return 0, domain.ErrInvalidTTL
	}
	return ttl, nil
}

func createAcceptedResponse(sandbox domain.Sandbox) (storeport.IdempotentResponse, error) {
	mapped := protocol.Sandbox{
		ID: sandbox.ID, State: protocol.SandboxStatePending,
		Reason: protocol.SandboxReasonCreateAccepted, Message: sandbox.Message,
		Image: sandbox.Spec.Image, ExpiresAt: *sandbox.ExpiresAt,
		CreatedAt: sandbox.CreatedAt, UpdatedAt: sandbox.UpdatedAt,
	}
	body, err := json.Marshal(mapped)
	if err != nil {
		return storeport.IdempotentResponse{}, fmt.Errorf("encode accepted sandbox response: %w", err)
	}
	return storeport.IdempotentResponse{
		SchemaVersion: 1, StatusCode: http.StatusAccepted,
		Location: "/v1/sandboxes/" + sandbox.ID, Body: body, CreatedAt: sandbox.CreatedAt,
	}, nil
}
