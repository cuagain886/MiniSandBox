package sdk

import "minisandbox/pkg/protocol"

// ExecutionEvent 是 Go SDK 向调用方交付的稳定执行事件模型。
//
// 该别名保持 SDK 与公共 SSE/日志 JSON 契约完全一致；事件载荷中的输出仍为
// Base64 字符串，调用方可按需解码为原始字节。
type ExecutionEvent = protocol.ExecutionEvent

// EventType 是 Go SDK 暴露的稳定执行事件类型。
type EventType = protocol.EventType
