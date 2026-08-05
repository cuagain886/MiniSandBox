package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestEventStoreRetainsExactlyOutputBudget 验证恰好达到上限不会误报截断，terminal 始终保留。
func TestEventStoreRetainsExactlyOutputBudget(t *testing.T) {
	store := newStartedEventStore(t, 5)
	defer store.Close()
	if err := store.AppendOutput(context.Background(), RawOutputChunk{Stream: OutputStreamStdout, Data: []byte("12345")}); err != nil {
		t.Fatalf("append output: %v", err)
	}
	publishExited(t, store, 0)
	events := store.Events()
	if len(events) != 3 || events[1].Type != protocol.EventStdout || decodeEventData(t, events[1]) != "12345" || events[2].Type != protocol.EventExited || *events[2].OutputTruncated {
		t.Fatalf("events: %+v", events)
	}
}

// TestEventStoreSharesBudgetAcrossStreamsAndPublishesLimitOnce 验证跨 chunk 截断、双 stream 共用预算和后续输出丢弃。
func TestEventStoreSharesBudgetAcrossStreamsAndPublishesLimitOnce(t *testing.T) {
	store := newStartedEventStore(t, 5)
	defer store.Close()
	chunks := []RawOutputChunk{
		{Stream: OutputStreamStdout, Data: []byte("abc")},
		{Stream: OutputStreamStderr, Data: []byte("WXYZ")},
		{Stream: OutputStreamStdout, Data: []byte("discarded")},
	}
	for _, chunk := range chunks {
		if err := store.AppendOutput(context.Background(), chunk); err != nil {
			t.Fatalf("append output: %v", err)
		}
	}
	publishExited(t, store, 9)
	events := store.Events()
	wantTypes := []protocol.EventType{protocol.EventStarted, protocol.EventStdout, protocol.EventStderr, protocol.EventOutputLimitReached, protocol.EventExited}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count: got %d, want %d: %+v", len(events), len(wantTypes), events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want || events[index].Sequence != uint64(index+1) {
			t.Fatalf("event[%d]: %+v, want type %q", index, events[index], want)
		}
	}
	if decodeEventData(t, events[1]) != "abc" || decodeEventData(t, events[2]) != "WX" || !*events[4].OutputTruncated {
		t.Fatalf("retained output or terminal marker: %+v", events)
	}
	if err := store.AppendOutput(context.Background(), RawOutputChunk{Stream: OutputStreamStdout, Data: []byte("late")}); !errors.Is(err, ErrEventStoreTerminal) {
		t.Fatalf("output after terminal: %v", err)
	}
}

// TestEventStoreZeroOutputAndControlEventsDoNotSpendBudget 验证无输出执行只保存控制事件，terminal 不受输出预算影响。
func TestEventStoreZeroOutputAndControlEventsDoNotSpendBudget(t *testing.T) {
	store := newStartedEventStore(t, 1)
	defer store.Close()
	publishExited(t, store, 127)
	events := store.Events()
	if len(events) != 2 || events[0].Type != protocol.EventStarted || events[1].Type != protocol.EventExited || *events[1].OutputTruncated {
		t.Fatalf("zero-output events: %+v", events)
	}
}

// TestEventStoreEventsReturnsDeepCopy 验证调用方不能通过 terminal pointer 修改内部快照。
func TestEventStoreEventsReturnsDeepCopy(t *testing.T) {
	store := newStartedEventStore(t, 1)
	defer store.Close()
	publishExited(t, store, 1)
	first := store.Events()
	*first[1].ExitCode = 99
	*first[1].OutputTruncated = true
	second := store.Events()
	if *second[1].ExitCode != 1 || *second[1].OutputTruncated {
		t.Fatalf("store snapshot was mutated: %+v", second[1])
	}
}

func newStartedEventStore(t *testing.T, budget int64) *EventStore {
	t.Helper()
	store, err := NewEventStore("exec_test", fixedClock{value: time.Now()}, budget)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	if _, err := store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted}); err != nil {
		store.Close()
		t.Fatalf("publish started: %v", err)
	}
	return store
}

func publishExited(t *testing.T, store *EventStore, exitCode int) {
	t.Helper()
	duration := int64(1)
	if _, err := store.PublishControl(context.Background(), protocol.ExecutionEvent{
		Type:       protocol.EventExited,
		ExitCode:   &exitCode,
		DurationMS: &duration,
	}); err != nil {
		t.Fatalf("publish exited: %v", err)
	}
}

func decodeEventData(t *testing.T, event protocol.ExecutionEvent) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(event.DataBase64)
	if err != nil {
		t.Fatalf("decode event data: %v", err)
	}
	return string(data)
}
