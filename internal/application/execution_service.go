package application

import (
	"context"

	"minisandbox/internal/domain"
)

// ExecutionGateway 定义应用层访问容器内 runner 所需的最小能力。
type ExecutionGateway interface {
	// Execute 向指定 sandbox 提交执行请求。
	Execute(context.Context, string, domain.ExecutionSpec) error
	// Cancel 取消指定 sandbox 中的一次执行；重复取消必须保持幂等。
	Cancel(context.Context, string, string) error
}

// ExecutionService 编排命令校验和 runner 调用。
type ExecutionService struct {
	gateway ExecutionGateway
}

// NewExecutionService 使用给定执行端口创建应用服务。
func NewExecutionService(gateway ExecutionGateway) *ExecutionService {
	return &ExecutionService{gateway: gateway}
}

// Execute 校验 argv 与 shell 的互斥关系，然后将命令交给当前 sandbox 的 runner。
func (s *ExecutionService) Execute(ctx context.Context, command Execute) error {
	if !command.Spec.Valid() {
		return domain.ErrInvalidExecutionRequest
	}
	return s.gateway.Execute(ctx, command.SandboxID, command.Spec)
}
