package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"minisandbox/pkg/protocol"
)

// 前台 SSE 解码的缓冲上限与控制面 runnerclient 保持一致，防止服务端异常
// 时客户端无界占用内存。
const (
	maxSSELineBytes  = 64 << 10
	maxSSEFrameBytes = 128 << 10
)

// ExecuteStream 在当前 sandbox 中启动前台 execution 并返回 SSE 事件迭代器。
//
// 调用方按 Next/Event 逐条消费已解码事件；流以唯一终止事件结束，其后
// Next 返回 false。迭代器持有 HTTP 响应体，终止事件抵达或发生错误时自动
// 释放；提前放弃流式执行时必须调用 Close。
func (s *Sandbox) ExecuteStream(
	ctx context.Context,
	request ExecuteRequest,
) (*EventStream, error) {
	wire, err := request.wire(false)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(wire); err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.client.baseURL+executionCollectionPath(s.id),
		&body,
	)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")

	response, err := s.client.httpClient.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, responseError(response.StatusCode, response.Body)
	}
	return &EventStream{
		body:   response.Body,
		reader: bufio.NewReaderSize(response.Body, 4096),
	}, nil
}

// EventStream 是前台 execution SSE 流的已解码事件迭代器。
type EventStream struct {
	body   io.ReadCloser
	reader *bufio.Reader

	current          DecodedEvent
	executionID      string
	expectedSequence uint64
	terminal         bool
	err              error
}

// Event 返回最近一次 Next 成功时抵达的事件。
func (s *EventStream) Event() DecodedEvent {
	return s.current
}

// Err 返回流式过程中发生的传输或协议错误；正常终止后返回 nil。
func (s *EventStream) Err() error {
	return s.err
}

// Close 立即放弃流式执行并释放底层 HTTP 响应。
//
// 未读到终止事件就关闭等价于客户端断开，服务端会按取消语义终止进程组。
func (s *EventStream) Close() error {
	return s.body.Close()
}

// Next 推进到下一条事件；终止事件已交付、发生错误或流提前结束时返回
// false。
func (s *EventStream) Next() bool {
	if s.err != nil || s.terminal {
		return false
	}
	event, hasEvent, err := s.readEvent()
	if err != nil {
		s.err = err
		_ = s.body.Close()
		return false
	}
	if !hasEvent {
		s.err = errors.New("minisandbox: execution stream ended without a terminal event")
		_ = s.body.Close()
		return false
	}
	s.current = event
	if event.Terminal() {
		s.terminal = true
		_ = s.body.Close()
	}
	return true
}

// readEvent 读取直到下一条业务事件；返回 hasEvent 为 false 且 err 为 nil
// 仅表示流正常耗尽。注释行和空帧按心跳跳过。
func (s *EventStream) readEvent() (DecodedEvent, bool, error) {
	var dataLines []string
	var frameID, frameEvent string
	frameBytes := 0
	hasField := false
	for {
		line, eof, err := readSSELine(s.reader)
		if err != nil {
			return DecodedEvent{}, false, err
		}
		if len(line) > 0 {
			if len(line) > maxSSEFrameBytes-frameBytes {
				return DecodedEvent{}, false, errors.New("minisandbox: SSE frame exceeds limit")
			}
			frameBytes += len(line) + 1
			text := string(line)
			if strings.HasPrefix(text, ":") {
				continue
			}
			name, value, _ := strings.Cut(text, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "data":
				dataLines = append(dataLines, value)
				hasField = true
			case "event":
				frameEvent = value
				hasField = true
			case "id":
				frameID = value
				hasField = true
			}
			continue
		}
		// 空行是 frame 边界；空 frame 或纯注释 frame 是心跳，跳过。
		if !hasField {
			if eof {
				return DecodedEvent{}, false, nil
			}
			dataLines = nil
			frameID = ""
			frameEvent = ""
			frameBytes = 0
			continue
		}
		event, err := decodeSSEData(strings.Join(dataLines, "\n"), frameEvent)
		if err != nil {
			return DecodedEvent{}, false, err
		}
		if frameID != "" {
			sequence, err := parseSSESequence(frameID)
			if err != nil {
				return DecodedEvent{}, false, err
			}
			if sequence != event.Sequence {
				return DecodedEvent{}, false, errors.New("minisandbox: SSE id disagrees with payload sequence")
			}
		}
		// 新 execution 的序号从 1 开始连续递增；execution ID 中途变化说明
		// 流被错误复用，必须立即失败而不是拼接两段输出。
		if s.executionID == "" {
			s.executionID = event.ExecutionID
			s.expectedSequence = 1
		}
		if event.ExecutionID != s.executionID || event.Sequence != s.expectedSequence {
			return DecodedEvent{}, false, fmt.Errorf(
				"minisandbox: execution stream broke at sequence %d for %s",
				event.Sequence,
				event.ExecutionID,
			)
		}
		s.expectedSequence++
		return event, true, nil
	}
}

// decodeSSEData 把单帧 data JSON 解码为已验证的 SDK 事件。
func decodeSSEData(data string, frameEvent string) (DecodedEvent, error) {
	if data == "" {
		return DecodedEvent{}, errors.New("minisandbox: SSE frame has no data")
	}
	var wireEvent protocol.ExecutionEvent
	if err := json.Unmarshal([]byte(data), &wireEvent); err != nil {
		return DecodedEvent{}, fmt.Errorf("minisandbox: decode SSE event: %w", err)
	}
	if err := wireEvent.Validate(); err != nil {
		return DecodedEvent{}, fmt.Errorf(
			"minisandbox: invalid SSE event payload: %w", err,
		)
	}
	if frameEvent != "" && frameEvent != string(wireEvent.Type) {
		return DecodedEvent{}, errors.New("minisandbox: SSE event field disagrees with payload")
	}
	return newDecodedEvent(wireEvent)
}

// readSSELine 读取一行并在超过缓冲上限时失败，语义与 runnerclient 一致。
func readSSELine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maxSSELineBytes-len(line) {
			return nil, false, errors.New("minisandbox: SSE line exceeds limit")
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

// parseSSESequence 校验 id 字段是规范十进制 sequence。
func parseSSESequence(id string) (uint64, error) {
	sequence, err := strconv.ParseUint(id, 10, 64)
	if err != nil || strconv.FormatUint(sequence, 10) != id {
		return 0, errors.New("minisandbox: invalid SSE id")
	}
	return sequence, nil
}
