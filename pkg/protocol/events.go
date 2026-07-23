package protocol

import "encoding/json"

type EventType string

const (
	EventStarted EventType = "started"
	EventStdout  EventType = "stdout"
	EventStderr  EventType = "stderr"
	EventExited  EventType = "exited"
	EventError   EventType = "error"
)

type ExecutionEvent struct {
	Sequence uint64          `json:"sequence"`
	Type     EventType       `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
}
