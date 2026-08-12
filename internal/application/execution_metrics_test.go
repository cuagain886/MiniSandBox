package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/testutil"
	"minisandbox/pkg/protocol"
)

type executionMetricsFake struct {
	mu        sync.Mutex
	requests  [][2]string
	terminals []string
}

func (m *executionMetricsFake) ObserveExecutionRequest(mode, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, [2]string{mode, result})
}
func (m *executionMetricsFake) ObserveForegroundTerminal(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminals = append(m.terminals, result)
}

type metricEventStream struct {
	events []protocol.ExecutionEvent
	err    error
}

func (s *metricEventStream) Consume(consume func(protocol.ExecutionEvent) error) error {
	for _, event := range s.events {
		if err := consume(event); err != nil {
			return err
		}
	}
	return s.err
}
func (*metricEventStream) Close() error { return nil }

// TestMetricsExecutionServiceCountsRequestBranches 验证前后台接受、确定性拒绝和内部错误各只计一次。
func TestMetricsExecutionServiceCountsRequestBranches(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(runningSandbox(), nil)
	client := &executionClientFake{stream: &metricEventStream{}, descriptor: ExecutionDescriptor{ID: "exec_metric", State: protocol.ExecutionStateRunning}}
	base, _ := NewExecutionService(storeFake, &executionFactoryFake{client: client})
	metrics := &executionMetricsFake{}
	service, _ := NewMetricsExecutionService(base, metrics)
	_, _ = service.Execute(context.Background(), validExecutionCommand(false))
	_, _ = service.Execute(context.Background(), validExecutionCommand(true))
	storeFake.SetGetResult(domain.Sandbox{ID: executionServiceSandboxID, DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending}, nil)
	_, _ = service.Execute(context.Background(), validExecutionCommand(false))
	storeFake.SetGetResult(domain.Sandbox{}, errors.New("store unavailable"))
	_, _ = service.Execute(context.Background(), validExecutionCommand(false))
	want := [][2]string{{"foreground", "accepted"}, {"background", "accepted"}, {"foreground", "rejected"}, {"foreground", "error"}}
	if len(metrics.requests) != len(want) {
		t.Fatalf("requests: %#v", metrics.requests)
	}
	for index := range want {
		if metrics.requests[index] != want[index] {
			t.Fatalf("request %d: got=%v want=%v", index, metrics.requests[index], want[index])
		}
	}
}

// TestMetricsExecutionStreamCountsOnlyFirstValidTerminal 验证提前断连、无终态与重复终态不会重复计数。
func TestMetricsExecutionStreamCountsOnlyFirstValidTerminal(t *testing.T) {
	metrics := &executionMetricsFake{}
	duration, truncated, exitCode := int64(5), false, 0
	terminal := protocol.ExecutionEvent{ExecutionID: "exec_metric", Sequence: 1, Timestamp: time.Now().UTC(), Type: protocol.EventExited,
		ExitCode: &exitCode, DurationMS: &duration, OutputTruncated: &truncated}
	wrapped := &terminalObservingStream{next: &metricEventStream{events: []protocol.ExecutionEvent{terminal, terminal}}, metrics: metrics}
	if err := wrapped.Consume(func(protocol.ExecutionEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Consume(func(protocol.ExecutionEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(metrics.terminals) != 1 || metrics.terminals[0] != "exited" {
		t.Fatalf("terminals: %#v", metrics.terminals)
	}

	disconnected := &terminalObservingStream{next: &metricEventStream{err: context.Canceled}, metrics: metrics}
	_ = disconnected.Consume(func(protocol.ExecutionEvent) error { return nil })
	invalid := terminal
	invalid.Timestamp = time.Time{}
	invalidStream := &terminalObservingStream{next: &metricEventStream{events: []protocol.ExecutionEvent{invalid}}, metrics: metrics}
	_ = invalidStream.Consume(func(protocol.ExecutionEvent) error { return nil })
	if len(metrics.terminals) != 1 {
		t.Fatalf("unexpected terminal observations: %#v", metrics.terminals)
	}
}
