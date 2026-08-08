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

func TestExecutionServiceStatusUsesSandboxBoundClient(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(runningSandbox(), nil)
	want := ExecutionStatus{Descriptor: ExecutionDescriptor{ID: "exec_test", State: protocol.ExecutionStateRunning}}
	client := &executionClientFake{status: want}
	factory := &executionFactoryFake{client: client}
	service, _ := NewExecutionService(storeFake, factory)
	got, err := service.Status(context.Background(), executionServiceSandboxID, "exec_test")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("status: %+v err=%v", got, err)
	}
	if !reflect.DeepEqual(factory.ids, []string{executionServiceSandboxID}) || !reflect.DeepEqual(client.statusIDs, []string{"exec_test"}) {
		t.Fatalf("selection: sandboxes=%v executions=%v", factory.ids, client.statusIDs)
	}

	client.statusErr = domain.ErrExecutionNotFound
	if _, err := service.Status(context.Background(), executionServiceSandboxID, "missing"); !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Fatalf("not found mapping: %v", err)
	}
	storeFake.SetGetResult(domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredRunning, ObservedState: domain.StateStopping}, nil)
	before := len(client.statusIDs)
	if _, err := service.Status(context.Background(), executionServiceSandboxID, "exec_test"); !errors.Is(err, domain.ErrSandboxNotRunning) || len(client.statusIDs) != before {
		t.Fatalf("non-running query: err=%v calls=%v", err, client.statusIDs)
	}
}

func TestExecutionServiceCancelPreservesIdempotentDisposition(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(runningSandbox(), nil)
	client := &executionClientFake{cancelDisposition: CancelAccepted}
	factory := &executionFactoryFake{client: client}
	service, _ := NewExecutionService(storeFake, factory)
	for range 2 {
		disposition, err := service.Cancel(context.Background(), executionServiceSandboxID, "exec_test")
		if err != nil || disposition != CancelAccepted {
			t.Fatalf("accepted cancel: disposition=%s err=%v", disposition, err)
		}
	}
	client.cancelDisposition = CancelAlreadyTerminal
	if disposition, err := service.Cancel(context.Background(), executionServiceSandboxID, "exec_test"); err != nil || disposition != CancelAlreadyTerminal {
		t.Fatalf("terminal cancel: disposition=%s err=%v", disposition, err)
	}
	client.cancelErr = domain.ErrExecutionNotFound
	if _, err := service.Cancel(context.Background(), executionServiceSandboxID, "missing"); !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Fatalf("unknown cancel: %v", err)
	}
	storeFake.SetGetResult(domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredTerminated, ObservedState: domain.StateRunning}, nil)
	before := len(client.cancelIDs)
	if _, err := service.Cancel(context.Background(), executionServiceSandboxID, "exec_test"); !errors.Is(err, domain.ErrSandboxNotRunning) || len(client.cancelIDs) != before {
		t.Fatalf("deleting cancel: err=%v calls=%v", err, client.cancelIDs)
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
	stream            ExecutionEventStream
	descriptor        ExecutionDescriptor
	foregroundErr     error
	backgroundErr     error
	foregroundSpecs   []domain.ExecutionSpec
	backgroundSpecs   []domain.ExecutionSpec
	status            ExecutionStatus
	statusErr         error
	statusIDs         []string
	cancelDisposition CancelDisposition
	cancelErr         error
	cancelIDs         []string
}

func (c *executionClientFake) Status(_ context.Context, id string) (ExecutionStatus, error) {
	c.statusIDs = append(c.statusIDs, id)
	return c.status, c.statusErr
}

func (c *executionClientFake) Cancel(_ context.Context, id string) (CancelDisposition, error) {
	c.cancelIDs = append(c.cancelIDs, id)
	return c.cancelDisposition, c.cancelErr
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
