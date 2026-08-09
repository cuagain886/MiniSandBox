package bootstrap

import (
	"context"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// applicationExecutionFactory 把固定 Unix Socket runner factory 适配为 application 端口，不允许调用方提供 URL 或路径。
type applicationExecutionFactory struct {
	factory *runnerclient.Factory
}

func (f applicationExecutionFactory) Client(sandboxID string) (application.ExecutionClient, error) {
	client, err := f.factory.Client(sandboxID)
	if err != nil {
		return nil, err
	}
	return applicationExecutionClient{client: client}, nil
}

type applicationExecutionClient struct {
	client *runnerclient.Client
}

func (c applicationExecutionClient) ExecuteForeground(ctx context.Context, spec domain.ExecutionSpec) (application.ExecutionEventStream, error) {
	return c.client.ExecuteForeground(ctx, mapRunnerExecuteRequest(spec))
}

func (c applicationExecutionClient) ExecuteBackground(ctx context.Context, spec domain.ExecutionSpec) (application.ExecutionDescriptor, error) {
	descriptor, err := c.client.ExecuteBackground(ctx, mapRunnerExecuteRequest(spec))
	if err != nil {
		return application.ExecutionDescriptor{}, err
	}
	return application.ExecutionDescriptor{ID: descriptor.ExecutionID, State: descriptor.State}, nil
}

func (c applicationExecutionClient) Status(ctx context.Context, executionID string) (application.ExecutionStatus, error) {
	status, err := c.client.Status(ctx, executionID)
	if err != nil {
		return application.ExecutionStatus{}, err
	}
	return application.ExecutionStatus{
		Descriptor:    application.ExecutionDescriptor{ID: status.ExecutionID, State: status.State},
		TerminalEvent: status.TerminalEvent,
	}, nil
}

func (c applicationExecutionClient) Cancel(ctx context.Context, executionID string) (application.CancelDisposition, error) {
	disposition, err := c.client.Cancel(ctx, executionID)
	if err != nil {
		return "", err
	}
	return application.CancelDisposition(disposition), nil
}

func (c applicationExecutionClient) Logs(ctx context.Context, executionID string, cursor uint64) (application.ExecutionLogPage, error) {
	page, err := c.client.Logs(ctx, executionID, cursor)
	if err != nil {
		return application.ExecutionLogPage{}, err
	}
	return application.ExecutionLogPage{Events: page.Events, NextCursor: page.NextCursor, Complete: page.Complete}, nil
}

func (c applicationExecutionClient) NetworkNamespace(ctx context.Context) (string, error) {
	health, err := c.client.Health(ctx, runnerbootstrap.CurrentProtocolVersion)
	if err != nil {
		return "", err
	}
	return health.NetNSIdentity, nil
}

func mapRunnerExecuteRequest(spec domain.ExecutionSpec) protocol.ExecuteRequest {
	timeoutSeconds := int64(0)
	if spec.Timeout > 0 {
		timeoutSeconds = int64(spec.Timeout / time.Second)
	}
	return protocol.ExecuteRequest{
		Argv:           append([]string(nil), spec.Argv...),
		Shell:          spec.Shell,
		Env:            cloneExecutionEnvironment(spec.Env),
		Cwd:            spec.Cwd,
		TimeoutSeconds: timeoutSeconds,
	}
}

func cloneExecutionEnvironment(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
