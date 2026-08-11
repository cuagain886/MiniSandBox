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
	lease, err := s.resolveCreateLease(command.TTLSeconds)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	spec, err := s.specBuilder.Build(command)
	if err != nil {
		return IdempotentCreateOutcome{}, err
	}
	canonical, err := CanonicalizeCreateRequest(CanonicalCreateRequest{
		Image: command.Image, TTLSeconds: lease.CanonicalTTLSeconds, Outbound: command.Outbound,
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
	now := lease.Now
	expiresAt := lease.ExpiresAt
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

// resolvedCreateLease 保存一次 create 使用的相对租期、唯一时钟读数和绝对到期时间。
// CanonicalTTLSeconds 保留请求字段 presence，避免默认值调整改变既有幂等身份。
type resolvedCreateLease struct {
	TTL                 time.Duration
	CanonicalTTLSeconds *int64
	Now                 time.Time
	ExpiresAt           time.Time
}

// resolveCreateLease 校验相对 TTL，并用同一次 UTC 时钟读数计算绝对到期时间。
func (s *SandboxService) resolveCreateLease(seconds *int64) (resolvedCreateLease, error) {
	ttl := s.createPolicy.DefaultTTL
	var canonicalTTLSeconds *int64
	if seconds != nil {
		if *seconds <= 0 || *seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return resolvedCreateLease{}, domain.ErrInvalidTTL
		}
		ttl = time.Duration(*seconds) * time.Second
		value := *seconds
		canonicalTTLSeconds = &value
	}
	if ttl < s.createPolicy.MinimumTTL || ttl > s.createPolicy.MaximumTTL || ttl%time.Second != 0 {
		return resolvedCreateLease{}, domain.ErrInvalidTTL
	}
	now := s.clock.Now().UTC()
	expiresAt := now.Add(ttl)
	// 公共协议使用 RFC3339；year 超出四位范围时编码会失败，因此在任何
	// Store 或 hash 副作用之前显式拒绝，而不是依赖后续 JSON marshal 报错。
	if !expiresAt.After(now) || expiresAt.Year() < 0 || expiresAt.Year() > 9999 {
		return resolvedCreateLease{}, domain.ErrInvalidTTL
	}
	return resolvedCreateLease{
		TTL: ttl, CanonicalTTLSeconds: canonicalTTLSeconds,
		Now: now, ExpiresAt: expiresAt.UTC(),
	}, nil
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
