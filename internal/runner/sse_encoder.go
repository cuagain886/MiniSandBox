package runner

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"minisandbox/pkg/protocol"
)

// ErrInvalidSSEEvent 表示事件不能安全编码为 runner SSE frame。
var ErrInvalidSSEEvent = errors.New("invalid runner SSE event")

// SSEFlusher 在 frame 完整写入后立即把缓冲内容推给客户端，并报告 transport flush 错误。
type SSEFlusher interface {
	Flush() error
}

// SSEEncoder 将已验证的 execution event 写成严格 SSE frame，并在每个 frame 后立即 flush。
type SSEEncoder struct {
	writer  io.Writer
	flusher SSEFlusher
}

// NewSSEEncoder 创建严格 encoder；writer 与 flusher 通常由同一个 http.ResponseWriter 提供。
func NewSSEEncoder(writer io.Writer, flusher SSEFlusher) (*SSEEncoder, error) {
	if writer == nil || flusher == nil {
		return nil, errors.New("SSE encoder is not configured")
	}
	return &SSEEncoder{writer: writer, flusher: flusher}, nil
}

// WriteEvent 编码单个 event：id 等于十进制 sequence，event 等于稳定类型，data 是单行 JSON。
// 完整 frame 通过一次 Write 提交，避免部分字段成功后再拼接不一致内容。
func (e *SSEEncoder) WriteEvent(event protocol.ExecutionEvent) error {
	if e == nil || e.writer == nil || e.flusher == nil || event.Sequence == 0 || event.Validate() != nil {
		return ErrInvalidSSEEvent
	}
	data, err := json.Marshal(event)
	if err != nil {
		return ErrInvalidSSEEvent
	}
	frame := make([]byte, 0, len(data)+64)
	frame = append(frame, "id: "...)
	frame = strconv.AppendUint(frame, event.Sequence, 10)
	frame = append(frame, '\n')
	frame = append(frame, "event: "...)
	frame = append(frame, event.Type...)
	frame = append(frame, '\n')
	frame = append(frame, "data: "...)
	frame = append(frame, data...)
	frame = append(frame, '\n', '\n')
	count, err := e.writer.Write(frame)
	if err != nil {
		return err
	}
	if count != len(frame) {
		return io.ErrShortWrite
	}
	return e.flusher.Flush()
}

// WriteKeepalive 写入不占用 sequence 的固定 SSE comment，并立即 flush。
func (e *SSEEncoder) WriteKeepalive() error {
	if e == nil || e.writer == nil || e.flusher == nil {
		return errors.New("SSE encoder is unavailable")
	}
	const frame = ": keepalive\n\n"
	count, err := io.WriteString(e.writer, frame)
	if err != nil {
		return err
	}
	if count != len(frame) {
		return io.ErrShortWrite
	}
	return e.flusher.Flush()
}
