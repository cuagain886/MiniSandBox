package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

type advancingClock struct {
	mu      sync.Mutex
	current time.Time
}

func (c *advancingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.current
	c.current = c.current.Add(time.Millisecond)
	return value
}

// TestEventSequencerAssignsStartedAndConcurrentSequences 验证高并发输入仍得到从 1 开始无重复、无跳号的序列。
func TestEventSequencerAssignsStartedAndConcurrentSequences(t *testing.T) {
	clock := &advancingClock{current: time.Date(2026, 8, 5, 9, 0, 0, 0, time.FixedZone("test", 8*60*60))}
	sequencer, err := NewEventSequencer("exec_test", clock)
	if err != nil {
		t.Fatalf("new sequencer: %v", err)
	}
	defer sequencer.Close()
	started, err := sequencer.Publish(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	if err != nil || started.Sequence != 1 || started.ExecutionID != "exec_test" || started.Timestamp.Location() != time.UTC {
		t.Fatalf("started event: %+v err=%v", started, err)
	}
	const count = 256
	events := make(chan protocol.ExecutionEvent, count)
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(value byte) {
			defer wait.Done()
			event, err := sequencer.Publish(context.Background(), protocol.ExecutionEvent{
				Type:       protocol.EventStdout,
				DataBase64: base64.StdEncoding.EncodeToString([]byte{value}),
			})
			if err != nil {
				errorsFound <- err
				return
			}
			events <- event
		}(byte(index))
	}
	wait.Wait()
	close(events)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent publish: %v", err)
	}
	sequences := make([]int, 0, count)
	for event := range events {
		if event.ExecutionID != "exec_test" || event.Timestamp.Location() != time.UTC {
			t.Fatalf("event metadata: %+v", event)
		}
		sequences = append(sequences, int(event.Sequence))
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if want := index + 2; sequence != want {
			t.Fatalf("sequence[%d]: got %d, want %d", index, sequence, want)
		}
	}
}

// TestEventSequencerRejectsInvalidDraftWithoutGap 验证伪造元数据和错误首事件不消耗 sequence。
func TestEventSequencerRejectsInvalidDraftWithoutGap(t *testing.T) {
	clock := fixedClock{value: time.Now()}
	sequencer, err := NewEventSequencer("exec_test", clock)
	if err != nil {
		t.Fatalf("new sequencer: %v", err)
	}
	defer sequencer.Close()
	invalid := []protocol.ExecutionEvent{
		{Type: protocol.EventStdout, DataBase64: "YQ=="},
		{ExecutionID: "forged", Type: protocol.EventStarted},
		{Sequence: 9, Type: protocol.EventStarted},
		{Timestamp: time.Now(), Type: protocol.EventStarted},
	}
	for _, draft := range invalid {
		if _, err := sequencer.Publish(context.Background(), draft); !errors.Is(err, ErrInvalidEventDraft) {
			t.Fatalf("invalid draft accepted: %+v err=%v", draft, err)
		}
	}
	started, err := sequencer.Publish(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	if err != nil || started.Sequence != 1 {
		t.Fatalf("sequence gap after invalid drafts: event=%+v err=%v", started, err)
	}
}

// TestEventSequencerRejectsPublishAfterClose 验证关闭后不能再写入。
func TestEventSequencerRejectsPublishAfterClose(t *testing.T) {
	sequencer, err := NewEventSequencer("exec_test", fixedClock{value: time.Now()})
	if err != nil {
		t.Fatalf("new sequencer: %v", err)
	}
	sequencer.Close()
	sequencer.Close()
	if _, err := sequencer.Publish(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted}); !errors.Is(err, ErrEventSequencerClosed) {
		t.Fatalf("publish after close: %v", err)
	}
}

// TestEventSequencerAllowsFailedAsFirstEvent 验证校验或启动失败无需伪造 started，failed terminal 可直接使用 sequence 1。
func TestEventSequencerAllowsFailedAsFirstEvent(t *testing.T) {
	sequencer, err := NewEventSequencer("exec_test", fixedClock{value: time.Now()})
	if err != nil {
		t.Fatalf("new sequencer: %v", err)
	}
	defer sequencer.Close()
	duration := int64(0)
	truncated := false
	event, err := sequencer.Publish(context.Background(), protocol.ExecutionEvent{
		Type:            protocol.EventFailed,
		DurationMS:      &duration,
		OutputTruncated: &truncated,
		ErrorCode:       "START_FAILED",
		Message:         "execution could not start",
	})
	if err != nil || event.Sequence != 1 || event.Type != protocol.EventFailed {
		t.Fatalf("first failed event: %+v err=%v", event, err)
	}
}
