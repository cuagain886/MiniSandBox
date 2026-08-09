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

// ExecutionLogPage 是 application 重新验证后的后台事件页。
type ExecutionLogPage struct {
	// Events 是 cursor 之后按 sequence 连续排列的公共事件。
	Events []protocol.ExecutionEvent
	// NextCursor 是本页最后事件序号；空页保持请求 cursor。
	NextCursor uint64
	// Complete 表示本页已经包含唯一 terminal event。
	Complete bool
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
	// Logs 读取 runner 固定 page 上限内的 cursor 日志页。
	Logs(context.Context, string, uint64) (ExecutionLogPage, error)
}

// ExecutionAdmissionGate 是 outbound sandbox 在创建新 execution 前必须通过的
// 只读 runtime/runner 网络健康门禁；查询和取消已有 execution 不触发该门禁。
type ExecutionAdmissionGate interface {
	// Check 验证 sidecar、Docker network mode、attestation 与 runner netns 身份一致。
	Check(context.Context, domain.Sandbox, ExecutionClient) error
}

// ExecutionNetworkIdentityClient 是 admission gate 从已绑定 runner client 取得当前
// netns 身份的可选能力，不能接受外部 URL 或其他 sandbox ID。
type ExecutionNetworkIdentityClient interface {
	// NetworkNamespace 返回严格验证后的 linux-netns:<device>:<inode>。
	NetworkNamespace(context.Context) (string, error)
}

// Status 在确认 sandbox 仍可执行后查询其绑定 runner，execution ID 不能选择其他 client。
func (s *ExecutionService) Status(ctx context.Context, sandboxID, executionID string) (ExecutionStatus, error) {
	if executionID == "" {
		return ExecutionStatus{}, domain.ErrExecutionNotFound
	}
	client, _, err := s.runningClient(ctx, sandboxID)
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
	client, _, err := s.runningClient(ctx, sandboxID)
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

// Logs 查询后台日志并再次验证 runner 返回的 ID、sequence、cursor 和 terminal 语义。
func (s *ExecutionService) Logs(ctx context.Context, sandboxID, executionID string, cursor uint64, limit int) (ExecutionLogPage, error) {
	if s == nil || executionID == "" || limit < 0 || limit > s.maxLogPageEvents {
		return ExecutionLogPage{}, domain.ErrInvalidExecutionRequest
	}
	if limit == 0 {
		limit = s.maxLogPageEvents
	}
	client, _, err := s.runningClient(ctx, sandboxID)
	if err != nil {
		return ExecutionLogPage{}, err
	}
	page, err := client.Logs(ctx, executionID, cursor)
	if err != nil {
		return ExecutionLogPage{}, mapRunnerClientError(err)
	}
	if page.Events == nil || len(page.Events) > s.maxLogPageEvents || page.NextCursor < cursor {
		return ExecutionLogPage{}, domain.ErrRunnerProtocolMismatch
	}
	expected := cursor
	for index, event := range page.Events {
		if expected == ^uint64(0) {
			return ExecutionLogPage{}, domain.ErrRunnerProtocolMismatch
		}
		expected++
		if event.ExecutionID != executionID || event.Sequence != expected || event.Validate() != nil || index < len(page.Events)-1 && event.Terminal() {
			return ExecutionLogPage{}, domain.ErrRunnerProtocolMismatch
		}
	}
	if len(page.Events) == 0 && page.NextCursor != cursor || len(page.Events) > 0 && page.NextCursor != page.Events[len(page.Events)-1].Sequence {
		return ExecutionLogPage{}, domain.ErrRunnerProtocolMismatch
	}
	terminal := len(page.Events) > 0 && page.Events[len(page.Events)-1].Terminal()
	// 空页可能表示 cursor 已经指向先前读取的 terminal；非空页则必须能从
	// 当前页最后事件独立证明 complete，不能相信矛盾的 runner 标志。
	if len(page.Events) > 0 && page.Complete != terminal {
		return ExecutionLogPage{}, domain.ErrRunnerProtocolMismatch
	}
	if len(page.Events) > limit {
		page.Events = append([]protocol.ExecutionEvent(nil), page.Events[:limit]...)
		page.NextCursor = page.Events[len(page.Events)-1].Sequence
		page.Complete = page.Events[len(page.Events)-1].Terminal()
	}
	return page, nil
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
	store            store.Store
	factory          ExecutionClientFactory
	maxLogPageEvents int
	admissionGate    ExecutionAdmissionGate
}

// NewExecutionService 使用 Store 与 runner client factory 创建 execution 应用服务。
// 可选 maxLogPageEvents 只能提供一次且必须为正；省略时使用 256。
func NewExecutionService(s store.Store, factory ExecutionClientFactory, maxLogPageEvents ...int) (*ExecutionService, error) {
	return newExecutionService(s, factory, nil, maxLogPageEvents...)
}

// NewExecutionServiceWithAdmissionGate 创建启用 outbound execution 健康门禁的服务。
// gate 只影响 network.outbound=true 的新 Execute，network none 保持原行为。
func NewExecutionServiceWithAdmissionGate(s store.Store, factory ExecutionClientFactory, gate ExecutionAdmissionGate, maxLogPageEvents ...int) (*ExecutionService, error) {
	if gate == nil {
		return nil, errors.New("execution admission gate is not configured")
	}
	return newExecutionService(s, factory, gate, maxLogPageEvents...)
}

func newExecutionService(s store.Store, factory ExecutionClientFactory, gate ExecutionAdmissionGate, maxLogPageEvents ...int) (*ExecutionService, error) {
	if s == nil || factory == nil || len(maxLogPageEvents) > 1 {
		return nil, errors.New("execution service is not configured")
	}
	limit := 256
	if len(maxLogPageEvents) == 1 {
		limit = maxLogPageEvents[0]
	}
	if limit <= 0 {
		return nil, errors.New("execution log page limit is invalid")
	}
	return &ExecutionService{store: s, factory: factory, maxLogPageEvents: limit, admissionGate: gate}, nil
}

// Execute 只校验基础领域不变量和 sandbox admission，再原样转交命令规格。
func (s *ExecutionService) Execute(ctx context.Context, command Execute) (ExecutionResult, error) {
	if s == nil || !command.Spec.Valid() {
		return ExecutionResult{}, domain.ErrInvalidExecutionRequest
	}
	client, sandbox, err := s.runningClient(ctx, command.SandboxID)
	if err != nil {
		return ExecutionResult{}, err
	}
	if sandbox.Spec.Network.Outbound {
		if s.admissionGate == nil || s.admissionGate.Check(ctx, sandbox, client) != nil {
			return ExecutionResult{}, domain.ErrRunnerUnhealthy
		}
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

func (s *ExecutionService) runningClient(ctx context.Context, sandboxID string) (ExecutionClient, domain.Sandbox, error) {
	sandbox, err := s.store.Get(ctx, sandboxID)
	if err != nil {
		return nil, domain.Sandbox{}, err
	}
	// 删除意图优先于仍滞后的 Running observed state，避免删除窗口继续接收命令。
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState != domain.StateRunning {
		return nil, domain.Sandbox{}, domain.ErrSandboxNotRunning
	}
	client, err := s.factory.Client(sandboxID)
	if err != nil || client == nil {
		return nil, domain.Sandbox{}, domain.ErrRunnerUnhealthy
	}
	return client, sandbox, nil
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
