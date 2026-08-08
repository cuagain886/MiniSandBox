package application

import (
	"context"
	"errors"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// ExecutionEventStream 是 application 传递 runner typed event 的最小只读端口。
// Consume 返回后底层响应体必须关闭；Close 用于 handler 提前停止转发。
type ExecutionEventStream interface {
	Consume(func(protocol.ExecutionEvent) error) error
	Close() error
}

// ExecutionDescriptor 是后台创建用例返回的内部结果，不含 PID、socket 或 runner 身份。
type ExecutionDescriptor struct {
	// ID 是当前 sandbox 内后续查询和取消使用的 execution ID。
	ID string
	// State 是 runner 返回并由外层显式映射的稳定状态。
	State protocol.ExecutionState
}

// ExecutionStatus 是状态查询返回的内部快照。
type ExecutionStatus struct {
	// Descriptor 包含 execution ID 和当前稳定状态。
	Descriptor ExecutionDescriptor
	// TerminalEvent 只在终态中存在，并与 Descriptor 属于同一 execution。
	TerminalEvent *protocol.ExecutionEvent
}

// CancelDisposition 描述 runner 接受取消时观察到的幂等状态。
type CancelDisposition string

const (
	// CancelAccepted 表示活动 execution 的取消已被接受，但不等待终态。
	CancelAccepted CancelDisposition = "accepted"
	// CancelAlreadyTerminal 表示 execution 已终态，取消是成功 no-op。
	CancelAlreadyTerminal CancelDisposition = "already_terminal"
)

// ExecutionClient 定义 P2-056 创建 execution 所需的固定 runner 能力。
type ExecutionClient interface {
	// ExecuteForeground 启动前台 execution 并返回 typed stream。
	ExecuteForeground(context.Context, domain.ExecutionSpec) (ExecutionEventStream, error)
	// ExecuteBackground 启动后台 execution 并返回内部描述符。
	ExecuteBackground(context.Context, domain.ExecutionSpec) (ExecutionDescriptor, error)
	// Status 查询当前 client 所绑定 sandbox 内的 execution。
	Status(context.Context, string) (ExecutionStatus, error)
	// Cancel 幂等取消当前 client 所绑定 sandbox 内的 execution。
	Cancel(context.Context, string) (CancelDisposition, error)
}

// Status 在确认 sandbox 仍可执行后查询其绑定 runner，execution ID 不能选择其他 client。
func (s *ExecutionService) Status(ctx context.Context, sandboxID, executionID string) (ExecutionStatus, error) {
	if executionID == "" {
		return ExecutionStatus{}, domain.ErrExecutionNotFound
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return ExecutionStatus{}, err
	}
	status, err := client.Status(ctx, executionID)
	if err != nil {
		return ExecutionStatus{}, mapRunnerClientError(err)
	}
	if status.Descriptor.ID != executionID {
		return ExecutionStatus{}, domain.ErrRunnerUnhealthy
	}
	return status, nil
}

// Cancel 在 sandbox admission 后转发固定按 ID 取消，不接受 PID、signal 或 force 参数。
func (s *ExecutionService) Cancel(ctx context.Context, sandboxID, executionID string) (CancelDisposition, error) {
	if executionID == "" {
		return "", domain.ErrExecutionNotFound
	}
	client, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	disposition, err := client.Cancel(ctx, executionID)
	if err != nil {
		return "", mapRunnerClientError(err)
	}
	if disposition != CancelAccepted && disposition != CancelAlreadyTerminal {
		return "", domain.ErrRunnerUnhealthy
	}
	return disposition, nil
}

// ExecutionClientFactory 只允许按已通过 Store gate 的 sandbox ID 选择 runner。
type ExecutionClientFactory interface {
	// Client 返回绑定到指定 sandbox 的固定 client，不能接受 URL 或任意 path。
	Client(sandboxID string) (ExecutionClient, error)
}

// ExecutionResult 是前后台互斥的 execution 创建结果。
type ExecutionResult struct {
	// Stream 仅在前台模式成功时存在。
	Stream ExecutionEventStream
	// Descriptor 仅在后台模式成功时存在。
	Descriptor *ExecutionDescriptor
}

// ExecutionService 在 Store 生命周期 gate 后调用当前 sandbox 的固定 runner client。
type ExecutionService struct {
	store   store.Store
	factory ExecutionClientFactory
}

// NewExecutionService 使用 Store 与 runner client factory 创建 execution 应用服务。
func NewExecutionService(s store.Store, factory ExecutionClientFactory) (*ExecutionService, error) {
	if s == nil || factory == nil {
		return nil, errors.New("execution service is not configured")
	}
	return &ExecutionService{store: s, factory: factory}, nil
}

// Execute 只校验基础领域不变量和 sandbox admission，再原样转交命令规格。
func (s *ExecutionService) Execute(ctx context.Context, command Execute) (ExecutionResult, error) {
	if s == nil || !command.Spec.Valid() {
		return ExecutionResult{}, domain.ErrInvalidExecutionRequest
	}
	client, err := s.runningClient(ctx, command.SandboxID)
	if err != nil {
		return ExecutionResult{}, err
	}
	if command.Background {
		descriptor, err := client.ExecuteBackground(ctx, cloneExecutionSpec(command.Spec))
		if err != nil {
			return ExecutionResult{}, mapRunnerClientError(err)
		}
		if descriptor.ID == "" {
			return ExecutionResult{}, domain.ErrRunnerUnhealthy
		}
		return ExecutionResult{Descriptor: &descriptor}, nil
	}
	stream, err := client.ExecuteForeground(ctx, cloneExecutionSpec(command.Spec))
	if err != nil {
		return ExecutionResult{}, mapRunnerClientError(err)
	}
	if stream == nil {
		return ExecutionResult{}, domain.ErrRunnerUnhealthy
	}
	return ExecutionResult{Stream: stream}, nil
}

func (s *ExecutionService) runningClient(ctx context.Context, sandboxID string) (ExecutionClient, error) {
	sandbox, err := s.store.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	// 删除意图优先于仍滞后的 Running observed state，避免删除窗口继续接收命令。
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState != domain.StateRunning {
		return nil, domain.ErrSandboxNotRunning
	}
	client, err := s.factory.Client(sandboxID)
	if err != nil || client == nil {
		return nil, domain.ErrRunnerUnhealthy
	}
	return client, nil
}

func mapRunnerClientError(err error) error {
	for _, known := range []error{domain.ErrInvalidExecutionRequest, domain.ErrExecutionNotFound, domain.ErrExecutionLimitReached, domain.ErrShellNotFound, domain.ErrInvalidCWD, domain.ErrRunnerProtocolMismatch, domain.ErrRunnerUnhealthy} {
		if errors.Is(err, known) {
			return known
		}
	}
	return domain.ErrRunnerUnhealthy
}

func cloneExecutionSpec(spec domain.ExecutionSpec) domain.ExecutionSpec {
	spec.Argv = append([]string(nil), spec.Argv...)
	if spec.Env != nil {
		environment := make(map[string]string, len(spec.Env))
		for key, value := range spec.Env {
			environment[key] = value
		}
		spec.Env = environment
	}
	return spec
}
