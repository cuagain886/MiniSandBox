package protocol

import "errors"

// PTY WebSocket 消息的公共约束。
const (
	// PTYSubprotocol 是 PTY WebSocket 连接唯一接受的子协议。
	PTYSubprotocol = "minisandbox.pty.v1"
	// PTYMinCols 和 PTYMaxCols 限定终端列数范围。
	PTYMinCols, PTYMaxCols = 1, 500
	// PTYMinRows 和 PTYMaxRows 限定终端行数范围。
	PTYMinRows, PTYMaxRows = 1, 300
)

// PTY 消息类型常量。
const (
	// PTYMessageTypeStart 是客户端首条文本消息，启动 PTY 会话。
	PTYMessageTypeStart = "start"
	// PTYMessageTypeResize 是客户端文本消息，调整终端窗口。
	PTYMessageTypeResize = "resize"
	// PTYServerEventStarted 表示用户进程已在 PTY 上启动。
	PTYServerEventStarted = "started"
	// PTYServerEventTerminal 表示会话进入唯一终态，此后连接关闭。
	PTYServerEventTerminal = "terminal"
	// PTYServerEventError 表示会话层错误。
	PTYServerEventError = "error"
)

// ErrInvalidPTYMessage 表示 PTY 文本消息违反公共协议。
var ErrInvalidPTYMessage = errors.New("invalid PTY message")

// PTYStartRequest 是客户端在 WebSocket 建立后发送的首条文本消息。
type PTYStartRequest struct {
	// Type 固定为 "start"。
	Type string `json:"type"`
	// Argv 是不经 shell 解析的启动命令，至少一个元素。
	Argv []string `json:"argv"`
	// Cwd 是 workspace 相对工作目录；空表示根目录。
	Cwd string `json:"cwd,omitempty"`
	// Env 是本次命令附加的环境变量；runner 会过滤自身认证变量。
	Env map[string]string `json:"env,omitempty"`
	// Cols 是初始终端列数。
	Cols uint16 `json:"cols"`
	// Rows 是初始终端行数。
	Rows uint16 `json:"rows"`
	// TimeoutSeconds 是会话最长时长；零表示使用服务端默认值。
	TimeoutSeconds int64 `json:"timeout_seconds"`
}

// Validate 校验 start 消息的字段约束。
func (r PTYStartRequest) Validate() error {
	if r.Type != PTYMessageTypeStart || len(r.Argv) == 0 {
		return ErrInvalidPTYMessage
	}
	for _, argument := range r.Argv {
		if argument == "" {
			return ErrInvalidPTYMessage
		}
	}
	if r.Cwd != "" {
		if err := ValidateFilePath(r.Cwd); err != nil {
			return ErrInvalidPTYMessage
		}
	}
	if r.Cols < PTYMinCols || r.Cols > PTYMaxCols ||
		r.Rows < PTYMinRows || r.Rows > PTYMaxRows {
		return ErrInvalidPTYMessage
	}
	if r.TimeoutSeconds < 0 {
		return ErrInvalidPTYMessage
	}
	return nil
}

// PTYResizeRequest 是客户端调整终端窗口的文本消息。
type PTYResizeRequest struct {
	// Type 固定为 "resize"。
	Type string `json:"type"`
	// Cols 是新的终端列数。
	Cols uint16 `json:"cols"`
	// Rows 是新的终端行数。
	Rows uint16 `json:"rows"`
}

// Validate 校验 resize 消息的字段约束。
func (r PTYResizeRequest) Validate() error {
	if r.Type != PTYMessageTypeResize {
		return ErrInvalidPTYMessage
	}
	if r.Cols < PTYMinCols || r.Cols > PTYMaxCols ||
		r.Rows < PTYMinRows || r.Rows > PTYMaxRows {
		return ErrInvalidPTYMessage
	}
	return nil
}

// PTYServerEvent 是服务端文本消息：started、terminal 或 error。
//
// ExitCode 和 DurationMS 仅出现在 terminal 事件；ErrorCode 和 Message 仅
// 出现在 error 事件与失败的 terminal 事件，且内容不得包含命令或秘密。
type PTYServerEvent struct {
	// Type 是 started、terminal 或 error 之一。
	Type string `json:"type"`
	// ExitCode 是进程退出码；仅 terminal 事件且进程完成 wait 时出现。
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS 是会话从启动到终态的耗时毫秒数；仅 terminal 事件出现。
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// ErrorCode 是稳定机器可读错误码。
	ErrorCode string `json:"error_code,omitempty"`
	// Message 是安全的人类可读说明。
	Message string `json:"message,omitempty"`
}
