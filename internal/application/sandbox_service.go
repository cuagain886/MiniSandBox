package application

import (
	"context"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
)

// SandboxService 编排 sandbox 生命周期用例及其持久化访问。
type SandboxService struct {
	store store.Store
}

// NewSandboxService 使用给定持久化端口创建生命周期服务。
func NewSandboxService(s store.Store) *SandboxService {
	return &SandboxService{store: s}
}

// Get 返回持久化的 sandbox 期望状态和最近一次观测状态。
func (s *SandboxService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	return s.store.Get(ctx, id)
}
