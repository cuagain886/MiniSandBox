package protocol

import "encoding/json"

// EventType 标识 SSE 执行事件的稳定类型。
type EventType string

const (
	// EventStarted 表示 runner 已成功启动用户进程。
	EventStarted EventType = "started"
	// EventStdout 携带用户进程的标准输出片段。
	EventStdout EventType = "stdout"
	// EventStderr 携带用户进程的标准错误片段。
	EventStderr EventType = "stderr"
	// EventExited 携带唯一且最终的退出结果。
	EventExited EventType = "exited"
	// EventError 表示执行启动或流式传输发生协议错误。
	EventError EventType = "error"
)

// ExecutionEvent 是 runner SSE 流中的单条有序事件。
type ExecutionEvent struct {
	// Sequence 是单次执行内从一开始单调递增的事件序号。
	Sequence uint64 `json:"sequence"`
	// Type 决定 Data 所使用的事件载荷结构。
	Type EventType `json:"type"`
	// Data 保存与事件类型对应的 JSON 载荷。
	Data json.RawMessage `json:"data,omitempty"`
}
