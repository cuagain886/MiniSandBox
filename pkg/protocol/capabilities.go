package protocol

// Capabilities 是 runner 实际提供的功能能力集合，控制面原样代理给调用方。
//
// 每个能力相互独立：files 为 false 时文件接口不可用，pty 为 false 时交互
// 终端不可用，http_port_proxy 为 false 时 loopback HTTP 代理不可用。调用方
// 必须把任一能力视为可选，而不是假定三者同时可用。
type Capabilities struct {
	// Files 表示 workspace 文件管理能力是否可用。
	Files bool `json:"files"`
	// PTY 表示交互式 PTY 会话能力是否可用。
	PTY bool `json:"pty"`
	// HTTPPortProxy 表示当前 sandbox loopback HTTP 代理能力是否可用。
	HTTPPortProxy bool `json:"http_port_proxy"`
}
