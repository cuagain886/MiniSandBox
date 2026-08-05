package runner

import (
	"context"
	"errors"
	"sync"

	"minisandbox/pkg/protocol"
)

// ErrEventSequencerClosed 表示 sequencer 已关闭，不能再分配事件序号。
var ErrEventSequencerClosed = errors.New("execution event sequencer is closed")

// ErrInvalidEventDraft 表示事件模板包含伪造元数据、顺序错误或字段组合无效。
var ErrInvalidEventDraft = errors.New("invalid execution event draft")

type eventSequenceRequest struct {
	draft    protocol.ExecutionEvent
	response chan eventSequenceResponse
}

type eventSequenceResponse struct {
	event protocol.ExecutionEvent
	err   error
}

// EventSequencer 通过单一 goroutine 为一个 execution 的事件分配连续序号和 UTC 时间戳。
type EventSequencer struct {
	executionID  ExecutionID
	clock        Clock
	requests     chan eventSequenceRequest
	closeOnce    sync.Once
	closeRequest chan struct{}
	done         chan struct{}
}

// NewEventSequencer 创建要求首个事件为 started 的中央 sequencer。
func NewEventSequencer(executionID ExecutionID, clock Clock) (*EventSequencer, error) {
	if executionID == "" || clock == nil {
		return nil, errors.New("event sequencer is not configured")
	}
	sequencer := &EventSequencer{
		executionID:  executionID,
		clock:        clock,
		requests:     make(chan eventSequenceRequest),
		closeRequest: make(chan struct{}),
		done:         make(chan struct{}),
	}
	go sequencer.run()
	return sequencer, nil
}

// Publish 串行补齐 execution ID、连续 sequence 和注入时钟；调用方不能预设这些字段。
func (s *EventSequencer) Publish(ctx context.Context, draft protocol.ExecutionEvent) (protocol.ExecutionEvent, error) {
	if s == nil {
		return protocol.ExecutionEvent{}, ErrEventSequencerClosed
	}
	response := make(chan eventSequenceResponse, 1)
	request := eventSequenceRequest{draft: draft, response: response}
	select {
	case <-ctx.Done():
		return protocol.ExecutionEvent{}, ctx.Err()
	case <-s.done:
		return protocol.ExecutionEvent{}, ErrEventSequencerClosed
	case s.requests <- request:
	}
	select {
	case <-ctx.Done():
		return protocol.ExecutionEvent{}, ctx.Err()
	case <-s.done:
		select {
		case result := <-response:
			return result.event, result.err
		default:
			return protocol.ExecutionEvent{}, ErrEventSequencerClosed
		}
	case result := <-response:
		return result.event, result.err
	}
}

// Close 串行关闭 sequencer；重复调用安全，返回后所有新 Publish 均失败。
func (s *EventSequencer) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.closeRequest) })
	<-s.done
}

func (s *EventSequencer) run() {
	defer close(s.done)
	var sequence uint64
	for {
		select {
		case <-s.closeRequest:
			return
		case request := <-s.requests:
			event, err := s.sequenceEvent(sequence+1, request.draft)
			if err == nil {
				sequence++
			}
			request.response <- eventSequenceResponse{event: event, err: err}
		}
	}
}

func (s *EventSequencer) sequenceEvent(sequence uint64, draft protocol.ExecutionEvent) (protocol.ExecutionEvent, error) {
	if draft.ExecutionID != "" || draft.Sequence != 0 || !draft.Timestamp.IsZero() {
		return protocol.ExecutionEvent{}, ErrInvalidEventDraft
	}
	if draft.Type == protocol.EventStarted && sequence != 1 {
		return protocol.ExecutionEvent{}, ErrInvalidEventDraft
	}
	// 校验或启动失败没有 started 事件，此时 failed terminal 自身使用 sequence 1；
	// stdout/stderr、limit、cancel、timeout 和 exited 均不允许抢占首事件。
	if sequence == 1 && draft.Type != protocol.EventStarted && draft.Type != protocol.EventFailed {
		return protocol.ExecutionEvent{}, ErrInvalidEventDraft
	}
	draft.ExecutionID = string(s.executionID)
	draft.Sequence = sequence
	draft.Timestamp = s.clock.Now().UTC()
	if draft.Timestamp.IsZero() || draft.Validate() != nil {
		return protocol.ExecutionEvent{}, ErrInvalidEventDraft
	}
	return draft, nil
}
