package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

// ServeConfigured 按目录、socket、降权、身份回验、readiness 的固定顺序启动 runner。
func ServeConfigured(ctx context.Context, version string, bootstrap runnerbootstrap.Config, token *runnerauth.Token) error {
	if token == nil {
		return errors.New("runner token is required")
	}
	defer token.Clear()
	if err := InitializeManagedDirectories(bootstrap); err != nil {
		_ = RestoreBootstrapDirectoryOwner(bootstrap)
		return err
	}
	executionDirectory, err := OpenManagedExecutionDirectory(bootstrap)
	if err != nil {
		_ = RestoreBootstrapDirectoryOwner(bootstrap)
		return err
	}
	defer executionDirectory.Close()
	listener, err := BindManagedSocket(bootstrap)
	if err != nil {
		return err
	}
	defer listener.Close()
	manager, err := NewManager(bootstrap.Limits.MaxConcurrentExecutions)
	if err != nil {
		return err
	}
	readiness := NewServerReadiness()
	status, err := NewExecutionStatusHandler(manager)
	if err != nil {
		return err
	}
	cancelHandler, err := NewExecutionCancelHandler(manager)
	if err != nil {
		return err
	}
	if err := DropPrivileges(listener, bootstrap.Identity); err != nil {
		return err
	}
	if err := VerifyRestrictedIdentity(bootstrap.Identity); err != nil {
		return err
	}
	reader, err := NewBackgroundLogReaderFromDirectory(executionDirectory, bootstrap.Limits.MaxLogPageEvents, bootstrap.Limits.MaxLogPageBytes)
	if err != nil {
		return err
	}
	logs, err := NewExecutionLogsHandler(manager, reader)
	if err != nil {
		return err
	}
	validator, err := NewRequestValidator(bootstrap.Limits)
	if err != nil {
		return err
	}
	launcher, err := NewExecutionLauncher(ctx, manager, bootstrap, executionDirectory)
	if err != nil {
		return err
	}
	stream, err := NewForegroundEventStream(ctx, manager, bootstrap.Limits.SSEWriteTimeoutNanoseconds, 15*time.Second)
	if err != nil {
		return err
	}
	create, err := NewExecutionCreateHandler(ExecutionCreateHandlerConfig{
		MaxRequestBytes:    bootstrap.Limits.MaxRequestBytes,
		Validator:          validator,
		ForegroundLauncher: launcher,
		ForegroundStream:   stream.Serve,
		ServerContext:      ctx,
		BackgroundLauncher: launcher,
	})
	if err != nil {
		return err
	}
	if err := readiness.MarkReady(); err != nil {
		return err
	}
	encodedToken := make([]byte, base64.RawURLEncoding.EncodedLen(len(token)))
	base64.RawURLEncoding.Encode(encodedToken, token[:])
	defer clear(encodedToken)
	routes := ServerRoutes{
		Create: create, Status: status, Cancel: cancelHandler, Logs: logs,
		Shutdown: NewShutdownHandler(manager, readiness, 5*time.Second),
	}
	handler, err := NewConfiguredServer(version, string(encodedToken), readiness, routes)
	if err != nil {
		return err
	}
	return ServeManaged(ctx, listener, handler, manager, readiness)
}
