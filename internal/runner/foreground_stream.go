package runner

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ForegroundEventStream 把内存事件源发送到前台 SSE 客户端，并把 transport 失败映射为前台断开取消。
type ForegroundEventStream struct {
	serverContext     context.Context
	manager           *Manager
	writeTimeout      time.Duration
	keepaliveInterval time.Duration
}

// NewForegroundEventStream 创建前台 stream loop；writeTimeout 和 keepaliveInterval 必须为正数。
func NewForegroundEventStream(
	serverContext context.Context,
	manager *Manager,
	writeTimeout time.Duration,
	keepaliveInterval time.Duration,
) (*ForegroundEventStream, error) {
	if serverContext == nil || manager == nil || writeTimeout <= 0 || keepaliveInterval <= 0 {
		return nil, errors.New("foreground event stream is not configured")
	}
	return &ForegroundEventStream{
		serverContext:     serverContext,
		manager:           manager,
		writeTimeout:      writeTimeout,
		keepaliveInterval: keepaliveInterval,
	}, nil
}

// Serve 设置 SSE headers 并持续发送到唯一 terminal；返回后不再写入 ResponseWriter。
func (s *ForegroundEventStream) Serve(w http.ResponseWriter, request *http.Request, handle *ExecutionHandle) {
	if s == nil || w == nil || request == nil || handle == nil || handle.Events == nil || handle.ExecutionID == "" {
		return
	}
	coordinator, err := StartForegroundCoordinator(s.serverContext, request.Context(), s.manager, handle.ExecutionID)
	if err != nil {
		_ = s.manager.requestCancellation(context.Background(), handle.ExecutionID, TerminationForegroundDisconnect)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	transport := &responseStreamTransport{writer: w, controller: controller}
	err = s.stream(request.Context(), handle, transport)
	_ = controller.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = s.manager.requestCancellation(context.Background(), handle.ExecutionID, TerminationForegroundDisconnect)
	}
	_ = coordinator.Wait(context.Background())
}

type eventStreamTransport interface {
	Write([]byte) (int, error)
	Flush() error
	SetWriteDeadline(time.Time) error
}

type responseStreamTransport struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func (t *responseStreamTransport) Write(data []byte) (int, error) { return t.writer.Write(data) }
func (t *responseStreamTransport) Flush() error                   { return t.controller.Flush() }
func (t *responseStreamTransport) SetWriteDeadline(deadline time.Time) error {
	return t.controller.SetWriteDeadline(deadline)
}

func (s *ForegroundEventStream) stream(ctx context.Context, handle *ExecutionHandle, transport eventStreamTransport) error {
	encoder, err := NewSSEEncoder(transport, transport)
	if err != nil {
		return err
	}
	cursor := uint64(0)
	for {
		events, terminal, changed := handle.Events.EventsAfter(cursor)
		for _, event := range events {
			if err := transport.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
				return err
			}
			if err := encoder.WriteEvent(event); err != nil {
				return err
			}
			cursor = event.Sequence
			if event.Terminal() {
				return nil
			}
		}
		if terminal {
			return errors.New("terminal event is missing from stream snapshot")
		}
		timer := time.NewTimer(s.keepaliveInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
			if err := transport.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
				return err
			}
			if err := encoder.WriteKeepalive(); err != nil {
				return err
			}
		}
	}
}
