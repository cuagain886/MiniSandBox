package runnerclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"minisandbox/pkg/protocol"
)

const (
	maxSSELineBytes  = 64 << 10
	maxSSEFrameBytes = 128 << 10
)

type sseFrame struct {
	id, event, data string
	hasID           bool
	hasEvent        bool
	hasData         bool
	commentOnly     bool
	bytes           int
}

// DecodeSSE 增量解码并验证一个完整 runner execution 事件流。
//
// 解码器限制单行和单 frame 缓冲，严格要求 id/event/data 各出现一次且与 JSON
// 一致，并验证 execution ID 固定、sequence 连续、terminal 唯一且为最后事件。
// reader 实现 io.Closer 时，本函数在成功、协议错误和 consumer 错误路径都会关闭它。
func DecodeSSE(reader io.Reader, consume func(protocol.ExecutionEvent) error) error {
	if reader == nil || consume == nil {
		return &ProtocolMismatchError{}
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	buffered := bufio.NewReaderSize(reader, 4096)
	frame := sseFrame{}
	executionID := ""
	expectedSequence := uint64(1)
	terminal := false
	seenEvent := false

	for {
		line, eof, err := readSSELine(buffered)
		if err != nil {
			return &ProtocolMismatchError{}
		}
		if len(line) == 0 {
			if frame.bytes > 0 {
				event, keepalive, err := decodeSSEFrame(frame)
				if err != nil {
					return &ProtocolMismatchError{}
				}
				if !keepalive {
					if terminal || event.Sequence != expectedSequence || (executionID != "" && event.ExecutionID != executionID) {
						return &ProtocolMismatchError{}
					}
					if executionID == "" {
						executionID = event.ExecutionID
					}
					seenEvent = true
					expectedSequence++
					terminal = event.Terminal()
					if err := consume(event); err != nil {
						return err
					}
				}
				frame = sseFrame{}
			}
		} else if err := appendSSELine(&frame, line); err != nil {
			return &ProtocolMismatchError{}
		}
		if eof {
			if frame.bytes != 0 || !seenEvent || !terminal {
				return &ProtocolMismatchError{}
			}
			return nil
		}
	}
}

func readSSELine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maxSSELineBytes-len(line) {
			return nil, false, errors.New("SSE line exceeds limit")
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			return nil, false, err
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		return line, eof, nil
	}
}

func appendSSELine(frame *sseFrame, line []byte) error {
	if frame == nil || len(line) > maxSSEFrameBytes-frame.bytes {
		return errors.New("SSE frame exceeds limit")
	}
	frame.bytes += len(line) + 1
	value := string(line)
	if strings.HasPrefix(value, ":") {
		if frame.hasID || frame.hasEvent || frame.hasData {
			return errors.New("SSE comment mixed with event")
		}
		frame.commentOnly = true
		return nil
	}
	if frame.commentOnly {
		return errors.New("SSE event mixed with comment")
	}
	name, data, found := strings.Cut(value, ":")
	if !found {
		return errors.New("SSE field is invalid")
	}
	data = strings.TrimPrefix(data, " ")
	switch name {
	case "id":
		if frame.hasID {
			return errors.New("duplicate SSE id")
		}
		frame.id, frame.hasID = data, true
	case "event":
		if frame.hasEvent {
			return errors.New("duplicate SSE event")
		}
		frame.event, frame.hasEvent = data, true
	case "data":
		if frame.hasData {
			return errors.New("duplicate SSE data")
		}
		frame.data, frame.hasData = data, true
	default:
		return errors.New("unknown SSE field")
	}
	return nil
}

func decodeSSEFrame(frame sseFrame) (protocol.ExecutionEvent, bool, error) {
	if frame.commentOnly && !frame.hasID && !frame.hasEvent && !frame.hasData {
		return protocol.ExecutionEvent{}, true, nil
	}
	if !frame.hasID || !frame.hasEvent || !frame.hasData || frame.id == "" || frame.event == "" || frame.data == "" {
		return protocol.ExecutionEvent{}, false, errors.New("incomplete SSE frame")
	}
	sequence, err := strconv.ParseUint(frame.id, 10, 64)
	if err != nil || strconv.FormatUint(sequence, 10) != frame.id {
		return protocol.ExecutionEvent{}, false, errors.New("invalid SSE id")
	}
	decoder := json.NewDecoder(strings.NewReader(frame.data))
	decoder.DisallowUnknownFields()
	var event protocol.ExecutionEvent
	if err := decoder.Decode(&event); err != nil {
		return protocol.ExecutionEvent{}, false, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || event.Validate() != nil {
		return protocol.ExecutionEvent{}, false, errors.New("invalid SSE data")
	}
	if event.Sequence != sequence || string(event.Type) != frame.event {
		return protocol.ExecutionEvent{}, false, errors.New("SSE fields disagree")
	}
	return event, false, nil
}
