package runner

import (
	"context"
	"errors"
	"os"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// ExecutionLauncher 把已校验请求组合成受 Manager 管理的真实用户进程，并统一驱动输出、超时、取消与 wait 收敛。
// 它不解析 HTTP，也不向调用方暴露 PID、PGID 或底层启动错误。
type ExecutionLauncher struct {
	serverContext      context.Context
	executionDirectory *os.File
	manager            *Manager
	bootstrap          runnerbootstrap.Config
	commandBuilder     *CommandBuilder
	environmentBuilder *EnvironmentBuilder
	imageEnvironment   []string
	clock              Clock
}

// NewExecutionLauncher 从可信 bootstrap 创建生产 launcher；image environment 会在每次执行前经过 denylist 清洗。
func NewExecutionLauncher(serverContext context.Context, manager *Manager, bootstrap runnerbootstrap.Config, executionDirectory *os.File) (*ExecutionLauncher, error) {
	if serverContext == nil || manager == nil || executionDirectory == nil {
		return nil, errors.New("execution launcher manager is required")
	}
	environmentBuilder, err := NewEnvironmentBuilder(bootstrap.Limits)
	if err != nil {
		return nil, err
	}
	if bootstrap.Paths.WorkspaceDirectory == "" || bootstrap.Limits.MaxOutputBytes <= 0 ||
		bootstrap.Limits.DefaultTimeoutNanoseconds <= 0 || bootstrap.Limits.TerminationGraceNanoseconds <= 0 {
		return nil, errors.New("execution launcher bootstrap is invalid")
	}
	return &ExecutionLauncher{
		serverContext:      serverContext,
		executionDirectory: executionDirectory,
		manager:            manager,
		bootstrap:          bootstrap,
		commandBuilder:     NewCommandBuilder(),
		environmentBuilder: environmentBuilder,
		imageEnvironment:   append([]string(nil), os.Environ()...),
		clock:              systemClock{},
	}, nil
}

// StartForeground 在返回前完成进程启动、事件源注册和取消入口绑定；请求 context 仅用于启动前中止检查。
// 启动成功后的前台生命周期由 ForegroundEventStream 映射，避免 request context 直接杀死进程而绕过终态裁决。
func (l *ExecutionLauncher) StartForeground(ctx context.Context, request ExecutionLaunchRequest) (*ExecutionHandle, error) {
	return l.start(ctx, request, false)
}

// StartBackground 启动不继承 HTTP request context 的后台 execution，并返回可供 status、logs 与 cancel 使用的描述符。
func (l *ExecutionLauncher) StartBackground(ctx context.Context, request ExecutionLaunchRequest) (ExecutionDescriptor, error) {
	handle, err := l.start(ctx, request, true)
	if err != nil {
		return ExecutionDescriptor{}, err
	}
	descriptor, err := l.manager.Descriptor(handle.ExecutionID)
	if err != nil {
		return ExecutionDescriptor{}, err
	}
	return descriptor, nil
}

func (l *ExecutionLauncher) start(ctx context.Context, request ExecutionLaunchRequest, background bool) (*ExecutionHandle, error) {
	if l == nil || l.manager == nil || l.commandBuilder == nil || l.environmentBuilder == nil || l.clock == nil {
		return nil, errors.New("execution launcher is not configured")
	}
	if ctx == nil {
		return nil, errors.New("execution launch context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cwd, err := ValidateCWD(l.bootstrap.Paths.WorkspaceDirectory, request.CWD)
	if err != nil {
		return nil, err
	}
	environment, err := l.environmentBuilder.Build(l.imageEnvironment, request.Env)
	if err != nil {
		return nil, err
	}
	spec, err := l.commandBuilder.Build(request.Validated, cwd, environment)
	if err != nil {
		return nil, err
	}
	execution, err := l.manager.CreateExecution()
	if err != nil {
		spec.Close()
		return nil, err
	}
	id := execution.Descriptor().ID
	store, err := NewEventStore(id, l.clock, l.bootstrap.Limits.MaxOutputBytes)
	if err != nil {
		spec.Close()
		l.completePendingFailure(execution, nil, TerminationInternalFailure, internalExecutionErrorCode, internalExecutionErrorMessage)
		return nil, err
	}
	if err := l.manager.SetEventStore(id, store); err != nil {
		spec.Close()
		l.completePendingFailure(execution, store, TerminationInternalFailure, internalExecutionErrorCode, internalExecutionErrorMessage)
		return nil, err
	}
	started, err := StartCommand(spec)
	if err != nil {
		l.completePendingFailure(execution, store, TerminationStartFailed, "EXECUTION_START_FAILED", "execution could not be started")
		return nil, err
	}
	readers, err := StartPipeReaders(started.Stdout, started.Stderr)
	if err != nil {
		_ = started.Command.Process.Kill()
		_ = started.Command.Wait()
		l.completePendingFailure(execution, store, TerminationInternalFailure, internalExecutionErrorCode, internalExecutionErrorMessage)
		return nil, err
	}
	startedAt := l.clock.Now().UTC()
	reaped := make(chan struct{})
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		_ = started.Command.Process.Kill()
		_ = WaitProcess(started.Command, readers.Results, startedAt, l.clock)
		close(reaped)
		l.completePendingFailure(execution, store, TerminationInternalFailure, internalExecutionErrorCode, internalExecutionErrorMessage)
		return nil, err
	}
	terminator, err := NewProcessGroupTerminator(started.PGID, l.bootstrap.Limits.TerminationGraceNanoseconds, reaped)
	if err != nil {
		_ = started.Command.Process.Kill()
		_ = WaitProcess(started.Command, readers.Results, startedAt, l.clock)
		close(reaped)
		close(finalized)
		_ = arbiter.Wait(context.Background())
		_ = l.manager.Complete(id)
		return nil, err
	}
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		_ = started.Command.Process.Kill()
		_ = WaitProcess(started.Command, readers.Results, startedAt, l.clock)
		close(reaped)
		close(finalized)
		_ = arbiter.Wait(context.Background())
		_ = l.manager.Complete(id)
		return nil, err
	}
	if _, err := store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted}); err != nil {
		_ = started.Command.Process.Kill()
		_ = WaitProcess(started.Command, readers.Results, startedAt, l.clock)
		close(reaped)
		_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(l.clock.Now().Sub(startedAt)))
		close(finalized)
		_ = arbiter.Wait(context.Background())
		_ = l.manager.Complete(id)
		return nil, err
	}
	cancelHandler := func(reason TerminationReason) error {
		duration := l.clock.Now().UTC().Sub(startedAt)
		if duration < 0 {
			duration = 0
		}
		decision, submitErr := arbiter.Submit(context.Background(), TerminalCandidate{Reason: reason, Duration: duration})
		if submitErr != nil {
			return submitErr
		}
		if decision.Won {
			if terminateErr := terminator.Terminate(); terminateErr != nil {
				return terminateErr
			}
		}
		return arbiter.Wait(context.Background())
	}
	if err := l.manager.SetCancellationHandler(id, cancelHandler); err != nil {
		_ = cancelHandler(TerminationInternalFailure)
		return nil, err
	}
	timeout, err := NewExecutionTimeout(request.Validated.Timeout, l.bootstrap.Limits.DefaultTimeoutNanoseconds, startedAt, arbiter, terminator)
	if err != nil {
		_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(0))
		_ = terminator.Terminate()
		return nil, err
	}
	var completionWait func(context.Context) error
	if background {
		writer, writerErr := NewBackgroundLogWriterFromDirectory(l.executionDirectory, id, store, arbiter)
		if writerErr != nil {
			go l.supervise(execution, store, started, readers, startedAt, reaped, finalized, arbiter, timeout, nil)
			_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(0))
			_ = terminator.Terminate()
			_ = arbiter.Wait(context.Background())
			return nil, writerErr
		}
		if _, coordinatorErr := StartBackgroundCoordinator(l.serverContext, l.manager, id); coordinatorErr != nil {
			go l.supervise(execution, store, started, readers, startedAt, reaped, finalized, arbiter, timeout, writer.Wait)
			_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(0))
			_ = terminator.Terminate()
			_ = arbiter.Wait(context.Background())
			return nil, coordinatorErr
		}
		completionWait = writer.Wait
	}
	go l.supervise(execution, store, started, readers, startedAt, reaped, finalized, arbiter, timeout, completionWait)
	return &ExecutionHandle{ExecutionID: id, Events: store}, nil
}

func (l *ExecutionLauncher) supervise(
	execution *Execution,
	store *EventStore,
	started StartedProcess,
	readers *PipeReaders,
	startedAt time.Time,
	reaped chan<- struct{},
	finalized chan<- struct{},
	arbiter *TerminalArbiter,
	timeout *ExecutionTimeout,
	completionWait func(context.Context) error,
) {
	outputDone := make(chan error, 1)
	go func() {
		var firstErr error
		for chunk := range readers.Chunks {
			if err := store.AppendOutput(context.Background(), chunk); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		outputDone <- firstErr
	}()
	outcome := WaitProcess(started.Command, readers.Results, startedAt, l.clock)
	close(reaped)
	outputErr := <-outputDone
	_ = timeout.Stop()
	if outputErr != nil {
		_, _ = arbiter.Submit(context.Background(), internalFailureCandidate(nonNegativeDuration(l.clock.Now().UTC().Sub(startedAt))))
	}
	if outcome.PipeFailure != nil {
		_, _ = arbiter.Submit(context.Background(), *outcome.PipeFailure)
	}
	_, _ = arbiter.Submit(context.Background(), outcome.WaitCandidate)
	close(finalized)
	_ = arbiter.Wait(context.Background())
	if completionWait != nil {
		_ = completionWait(context.Background())
	}
	_ = l.manager.Complete(execution.Descriptor().ID)
}

func (l *ExecutionLauncher) completePendingFailure(execution *Execution, store *EventStore, reason TerminationReason, code, message string) {
	if execution == nil {
		return
	}
	if err := execution.Transition(ExecutionFailed, reason, nil); err != nil {
		return
	}
	if store != nil {
		durationMS := int64(0)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{
			Type:       protocol.EventFailed,
			ErrorCode:  code,
			Message:    message,
			DurationMS: &durationMS,
		})
	}
	_ = l.manager.Complete(execution.Descriptor().ID)
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
