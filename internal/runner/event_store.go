package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"

	"minisandbox/pkg/protocol"
)

// ErrEventStoreTerminal 表示 terminal 已保存，后续事件不能再追加。
var ErrEventStoreTerminal = errors.New("execution event store is terminal")

// EventStore 保存单次 execution 的有序内存事件，并按原始 stdout/stderr bytes 共用输出预算。
type EventStore struct {
	mu              sync.RWMutex
	sequencer       *EventSequencer
	maxOutputBytes  int64
	outputBytes     int64
	outputTruncated bool
	limitPublished  bool
	terminal        bool
	events          []protocol.ExecutionEvent
	changed         chan struct{}
}

// NewEventStore 创建不自动发布 started 的事件存储；maxOutputBytes 必须为正数。
func NewEventStore(executionID ExecutionID, clock Clock, maxOutputBytes int64) (*EventStore, error) {
	if maxOutputBytes <= 0 {
		return nil, errors.New("execution output budget must be positive")
	}
	sequencer, err := NewEventSequencer(executionID, clock)
	if err != nil {
		return nil, err
	}
	return &EventStore{sequencer: sequencer, maxOutputBytes: maxOutputBytes, changed: make(chan struct{})}, nil
}

// PublishControl 保存 started 或 terminal 控制事件；terminal 的截断标志由 store 强制填写。
func (s *EventStore) PublishControl(ctx context.Context, draft protocol.ExecutionEvent) (protocol.ExecutionEvent, error) {
	if s == nil {
		return protocol.ExecutionEvent{}, ErrEventStoreTerminal
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return protocol.ExecutionEvent{}, ErrEventStoreTerminal
	}
	if draft.Type != protocol.EventStarted && !terminalEventType(draft.Type) {
		return protocol.ExecutionEvent{}, ErrInvalidEventDraft
	}
	if terminalEventType(draft.Type) {
		truncated := s.outputTruncated
		draft.OutputTruncated = &truncated
	}
	event, err := s.sequencer.Publish(ctx, draft)
	if err != nil {
		return protocol.ExecutionEvent{}, err
	}
	s.events = append(s.events, event)
	if event.Terminal() {
		s.terminal = true
	}
	s.notifyChangedLocked()
	return cloneExecutionEvent(event), nil
}

// AppendOutput 按 stdout/stderr 共享预算保存原始 chunk；超出部分丢弃且 limit 事件只发布一次。
func (s *EventStore) AppendOutput(ctx context.Context, chunk RawOutputChunk) error {
	if s == nil {
		return ErrEventStoreTerminal
	}
	if chunk.Stream != OutputStreamStdout && chunk.Stream != OutputStreamStderr {
		return ErrInvalidEventDraft
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return ErrEventStoreTerminal
	}
	before := len(s.events)
	remaining := s.maxOutputBytes - s.outputBytes
	retained := int64(len(chunk.Data))
	truncated := false
	if retained > remaining {
		retained = remaining
		truncated = true
	}
	if retained > 0 {
		eventType := protocol.EventStdout
		if chunk.Stream == OutputStreamStderr {
			eventType = protocol.EventStderr
		}
		event, err := s.sequencer.Publish(ctx, protocol.ExecutionEvent{
			Type:       eventType,
			DataBase64: base64.StdEncoding.EncodeToString(chunk.Data[:retained]),
		})
		if err != nil {
			return err
		}
		s.events = append(s.events, event)
		s.outputBytes += retained
	}
	if truncated {
		s.outputTruncated = true
		if !s.limitPublished {
			event, err := s.sequencer.Publish(ctx, protocol.ExecutionEvent{Type: protocol.EventOutputLimitReached})
			if err != nil {
				return err
			}
			s.events = append(s.events, event)
			s.limitPublished = true
		}
	}
	if len(s.events) != before {
		s.notifyChangedLocked()
	}
	return nil
}

// Events 返回深拷贝的有序事件快照，调用方不能修改 store 内部状态。
func (s *EventStore) Events() []protocol.ExecutionEvent {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.ExecutionEvent, len(s.events))
	for index, event := range s.events {
		result[index] = cloneExecutionEvent(event)
	}
	return result
}

// EventsAfter 返回 sequence 大于 cursor 的有序快照、当前 terminal 标志和下一次变更通知。
// publisher 只关闭并替换通知 channel，永远不等待 stream consumer。
func (s *EventStore) EventsAfter(cursor uint64) ([]protocol.ExecutionEvent, bool, <-chan struct{}) {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return nil, true, closed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	start := len(s.events)
	for index, event := range s.events {
		if event.Sequence > cursor {
			start = index
			break
		}
	}
	result := make([]protocol.ExecutionEvent, len(s.events)-start)
	for index, event := range s.events[start:] {
		result[index] = cloneExecutionEvent(event)
	}
	return result, s.terminal, s.changed
}

// Close 停止 sequencer；已保存事件仍可通过 Events 读取。
func (s *EventStore) Close() {
	if s != nil {
		s.sequencer.Close()
	}
}

func terminalEventType(eventType protocol.EventType) bool {
	switch eventType {
	case protocol.EventExited, protocol.EventFailed, protocol.EventCancelled, protocol.EventTimedOut:
		return true
	default:
		return false
	}
}

func cloneExecutionEvent(event protocol.ExecutionEvent) protocol.ExecutionEvent {
	if event.ExitCode != nil {
		value := *event.ExitCode
		event.ExitCode = &value
	}
	if event.DurationMS != nil {
		value := *event.DurationMS
		event.DurationMS = &value
	}
	if event.OutputTruncated != nil {
		value := *event.OutputTruncated
		event.OutputTruncated = &value
	}
	return event
}

func (s *EventStore) notifyChangedLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}
