package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/internal/testutil"
	"minisandbox/pkg/protocol"
)

const executionServiceSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

func TestExecutionServiceRequiresRunningDesiredSandbox(t *testing.T) {
	states := []domain.SandboxState{domain.StatePending, domain.StateCreating, domain.StateStopping, domain.StateTerminated, domain.StateFailed}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			storeFake := testutil.NewFakeStore()
			storeFake.SetGetResult(domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredRunning, ObservedState: state}, nil)
			factory := &executionFactoryFake{client: &executionClientFake{}}
			service, err := NewExecutionService(storeFake, factory)
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			_, err = service.Execute(context.Background(), validExecutionCommand(false))
			if !errors.Is(err, domain.ErrSandboxNotRunning) || len(factory.ids) != 0 {
				t.Fatalf("state %s admission: err=%v clients=%v", state, err, factory.ids)
			}
		})
	}

	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredTerminated, ObservedState: domain.StateRunning}, nil)
	factory := &executionFactoryFake{client: &executionClientFake{}}
	service, _ := NewExecutionService(storeFake, factory)
	if _, err := service.Execute(context.Background(), validExecutionCommand(false)); !errors.Is(err, domain.ErrSandboxNotRunning) {
		t.Fatalf("deleting Running sandbox admitted: %v", err)
	}
}

func TestExecutionServiceMapsStoreFactoryAndRunnerErrors(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(domain.Sandbox{}, domain.ErrNotFound)
	factory := &executionFactoryFake{client: &executionClientFake{}}
	service, _ := NewExecutionService(storeFake, factory)
	if _, err := service.Execute(context.Background(), validExecutionCommand(false)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("store not found classification: %v", err)
	}

	storeFake.SetGetResult(runningSandbox(), nil)
	factory.err = errors.New("socket path unavailable")
	if _, err := service.Execute(context.Background(), validExecutionCommand(false)); !errors.Is(err, domain.ErrRunnerUnhealthy) {
		t.Fatalf("factory classification: %v", err)
	}

	factory.err = nil
	factory.client = &executionClientFake{foregroundErr: domain.ErrRunnerProtocolMismatch}
	if _, err := service.Execute(context.Background(), validExecutionCommand(false)); !errors.Is(err, domain.ErrRunnerProtocolMismatch) {
		t.Fatalf("protocol classification: %v", err)
	}
	factory.client = &executionClientFake{foregroundErr: errors.New("secret internal failure")}
	if _, err := service.Execute(context.Background(), validExecutionCommand(false)); !errors.Is(err, domain.ErrRunnerUnhealthy) {
		t.Fatalf("unknown runner classification: %v", err)
	}
}

func TestExecutionServiceForwardsForegroundAndBackgroundWithoutMutation(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(runningSandbox(), nil)
	stream := &executionStreamFake{}
	client := &executionClientFake{stream: stream, descriptor: ExecutionDescriptor{ID: "exec_test", State: protocol.ExecutionStateRunning}}
	factory := &executionFactoryFake{client: client}
	service, _ := NewExecutionService(storeFake, factory)

	foreground := validExecutionCommand(false)
	result, err := service.Execute(context.Background(), foreground)
	if err != nil || result.Stream != stream || result.Descriptor != nil {
		t.Fatalf("foreground result: %+v err=%v", result, err)
	}
	background := validExecutionCommand(true)
	result, err = service.Execute(context.Background(), background)
	if err != nil || result.Stream != nil || result.Descriptor == nil || result.Descriptor.ID != "exec_test" {
		t.Fatalf("background result: %+v err=%v", result, err)
	}
	if !reflect.DeepEqual(client.foregroundSpecs, []domain.ExecutionSpec{foreground.Spec}) || !reflect.DeepEqual(client.backgroundSpecs, []domain.ExecutionSpec{background.Spec}) {
		t.Fatalf("spec mapping changed: foreground=%+v background=%+v", client.foregroundSpecs, client.backgroundSpecs)
	}
	if !reflect.DeepEqual(factory.ids, []string{executionServiceSandboxID, executionServiceSandboxID}) {
		t.Fatalf("factory sandbox selection: %v", factory.ids)
	}
}

func validExecutionCommand(background bool) Execute {
	return Execute{SandboxID: executionServiceSandboxID, Background: background, Spec: domain.ExecutionSpec{Argv: []string{"printf", "ok"}, Env: map[string]string{"A": "B"}, Cwd: "/workspace"}}
}

func runningSandbox() domain.Sandbox {
	return domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning}
}

type executionFactoryFake struct {
	client ExecutionClient
	err    error
	ids    []string
}

func (f *executionFactoryFake) Client(id string) (ExecutionClient, error) {
	f.ids = append(f.ids, id)
	return f.client, f.err
}

type executionClientFake struct {
	stream          ExecutionEventStream
	descriptor      ExecutionDescriptor
	foregroundErr   error
	backgroundErr   error
	foregroundSpecs []domain.ExecutionSpec
	backgroundSpecs []domain.ExecutionSpec
}

func (c *executionClientFake) ExecuteForeground(_ context.Context, spec domain.ExecutionSpec) (ExecutionEventStream, error) {
	c.foregroundSpecs = append(c.foregroundSpecs, spec)
	return c.stream, c.foregroundErr
}

func (c *executionClientFake) ExecuteBackground(_ context.Context, spec domain.ExecutionSpec) (ExecutionDescriptor, error) {
	c.backgroundSpecs = append(c.backgroundSpecs, spec)
	return c.descriptor, c.backgroundErr
}

type executionStreamFake struct{}

func (*executionStreamFake) Consume(func(protocol.ExecutionEvent) error) error { return nil }
func (*executionStreamFake) Close() error                                      { return nil }
