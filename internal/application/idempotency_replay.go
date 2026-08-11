package application

import (
	"context"
	"fmt"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// IdempotentCreateOutcome 是 application 向后续 HTTP adapter 提供的精确创建结果。
type IdempotentCreateOutcome struct {
	// SandboxID 是首次创建的稳定资源 ID，不使用本次候选 sandbox ID。
	SandboxID string
	// StatusCode 是首次保存的 HTTP status。
	StatusCode int
	// Location 是首次保存的 Location header。
	Location string
	// Body 是首次保存的原始 JSON bytes 副本，不从当前 domain state 重新编码。
	Body []byte
	// Replayed 表示 Store 命中已有相同请求。
	Replayed bool
}

// CommitIdempotentCreate 提交一次已准备完成的原子创建并保留 Store replay bytes。
//
// 本方法不装配到公共 Create handler，也不 Wake；这些副作用在 P3-027 统一接入，
// 以保证只有首次创建成功才唤醒 reconciler。
func (s *SandboxService) CommitIdempotentCreate(
	ctx context.Context,
	request storeport.IdempotentCreateRequest,
) (IdempotentCreateOutcome, error) {
	result, err := s.store.CreateIdempotent(ctx, request)
	if err != nil {
		return IdempotentCreateOutcome{}, fmt.Errorf("commit idempotent sandbox creation: %w", err)
	}
	return IdempotentCreateOutcome{
		SandboxID:  result.Sandbox.ID,
		StatusCode: result.Response.StatusCode,
		Location:   result.Response.Location,
		Body:       append([]byte(nil), result.Response.Body...),
		Replayed:   result.Replayed,
	}, nil
}

// CommitNonIdempotentCreate 提交一次无 key 创建，并复用首次创建响应 outcome。
//
// 本方法不生成任何内部 key，也不写 idempotency table；与 keyed 分支相同，
// HTTP 装配和 Wake 延后到 P3-027。
func (s *SandboxService) CommitNonIdempotentCreate(
	ctx context.Context,
	sandbox domain.Sandbox,
	response storeport.IdempotentResponse,
	maxSandboxes int,
) (IdempotentCreateOutcome, error) {
	created, err := s.store.CreateNonIdempotent(ctx, storeport.NonIdempotentCreateRequest{
		Sandbox: sandbox, MaxSandboxes: maxSandboxes,
	})
	if err != nil {
		return IdempotentCreateOutcome{}, fmt.Errorf("commit non-idempotent sandbox creation: %w", err)
	}
	return IdempotentCreateOutcome{
		SandboxID:  created.ID,
		StatusCode: response.StatusCode,
		Location:   response.Location,
		Body:       append([]byte(nil), response.Body...),
	}, nil
}
