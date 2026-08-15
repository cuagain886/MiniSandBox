package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerpty"
	"minisandbox/pkg/protocol"
)

// ptyHandshakeTimeout 是等待首条 start 消息的上限。
const ptyHandshakeTimeout = 10 * time.Second

// ptyMaxControlBytes 是单条文本控制消息的字节上限。
const ptyMaxControlBytes = 64 * 1024

// NewPTYHandler 返回 runner 内部 PTY WebSocket handler。
//
// 一条 WebSocket 连接恰好拥有一个 PTY 会话：首条文本消息必须是合法
// start 请求；此后客户端二进制帧是 stdin、文本帧是 resize，服务端二进制
// 帧是合并终端输出、文本帧是 started/terminal/error 事件。连接断开等价
// 于取消，进程组按 runner 统一语义终止。
func NewPTYHandler(
	manager *runnerpty.Manager,
	bootstrap runnerbootstrap.Config,
	serverContext context.Context,
) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("PTY manager is not configured")
	}
	if serverContext == nil {
		return nil, errors.New("PTY server context is not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{protocol.PTYSubprotocol},
		})
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusInternalError, "runner pty closed")
		if connection.Subprotocol() != protocol.PTYSubprotocol {
			_ = connection.Close(websocket.StatusPolicyViolation, "subprotocol required")
			return
		}
		connection.SetReadLimit(ptyMaxControlBytes)
		servePTYConnection(connection, manager, bootstrap, serverContext)
	}), nil
}

// servePTYConnection 执行一条连接的完整 PTY 生命周期。
func servePTYConnection(
	connection *websocket.Conn,
	manager *runnerpty.Manager,
	bootstrap runnerbootstrap.Config,
	serverContext context.Context,
) {
	handshakeContext, cancelHandshake := context.WithTimeout(serverContext, ptyHandshakeTimeout)
	defer cancelHandshake()
	messageType, reader, err := connection.Reader(handshakeContext)
	if err != nil {
		return
	}
	if messageType != websocket.MessageText {
		_ = connection.Close(websocket.StatusPolicyViolation, "first message must be start")
		return
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, ptyMaxControlBytes+1))
	if err != nil || len(encoded) > ptyMaxControlBytes {
		_ = connection.Close(websocket.StatusPolicyViolation, "oversized start message")
		return
	}
	var start protocol.PTYStartRequest
	decoder := json.NewDecoder(newByteReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&start); err != nil || decoder.Decode(new(any)) != io.EOF {
		writePTYErrorEvent(connection, "INVALID_PTY_REQUEST", "start message is invalid")
		return
	}

	session, err := manager.Start(serverContext, runnerpty.StartOptions{
		Request:          start,
		WorkspaceRoot:    bootstrap.Paths.WorkspaceDirectory,
		DefaultTimeout:   bootstrap.Features.PTYDefaultTimeoutNanoseconds,
		TerminationGrace: bootstrap.Limits.TerminationGraceNanoseconds,
		MaxEnvVars:       bootstrap.Limits.MaxEnvVars,
	})
	if err != nil {
		switch {
		case errors.Is(err, runnerpty.ErrInvalidStart):
			writePTYErrorEvent(connection, "INVALID_PTY_REQUEST", "start message is invalid")
		case errors.Is(err, runnerpty.ErrLimitReached):
			writePTYErrorEvent(connection, "PTY_LIMIT_REACHED", "PTY session limit has been reached")
		default:
			writePTYErrorEvent(connection, "PTY_UNAVAILABLE", "PTY capability is unavailable")
		}
		return
	}

	if err := writePTYEvent(connection, protocol.PTYServerEvent{Type: protocol.PTYServerEventStarted}); err != nil {
		session.Cancel(runnerpty.TerminalCauseCancelled)
		return
	}

	// 连接级 context：客户端断开或 runner 关闭都会取消会话。
	connectionContext, cancelConnection := context.WithCancel(serverContext)
	defer cancelConnection()
	writerDone := make(chan struct{})
	go pumpPTYOutput(connection, session, writerDone)

	readErr := readPTYInput(connectionContext, connection, session)
	session.Cancel(runnerpty.TerminalCauseCancelled)
	<-writerDone
	_ = readErr
}

// readPTYInput 循环消费客户端帧：二进制写入 stdin，文本按 resize 处理。
func readPTYInput(ctx context.Context, connection *websocket.Conn, session *runnerpty.Session) error {
	for {
		messageType, reader, err := connection.Reader(ctx)
		if err != nil {
			return err
		}
		switch messageType {
		case websocket.MessageBinary:
			chunk, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
			if readErr != nil {
				return readErr
			}
			// 进程退出后的 EIO 不是协议错误；终态由会话仲裁。
			_ = session.WriteStdin(chunk)
		case websocket.MessageText:
			encoded, readErr := io.ReadAll(io.LimitReader(reader, ptyMaxControlBytes+1))
			if readErr != nil || len(encoded) > ptyMaxControlBytes {
				return errors.New("oversized control message")
			}
			var resize protocol.PTYResizeRequest
			decoder := json.NewDecoder(newByteReader(encoded))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&resize); err != nil || decoder.Decode(new(any)) != io.EOF ||
				resize.Validate() != nil {
				return errors.New("invalid resize message")
			}
			_ = session.Resize(resize.Cols, resize.Rows)
		default:
			return errors.New("unexpected message type")
		}
	}
}

// pumpPTYOutput 是唯一写者：串行写出输出块与唯一 terminal 事件。
//
// 每次写都有独立超时；客户端长期停滞会导致写失败并按取消终止会话，
// 不缓存无界输出。
func pumpPTYOutput(connection *websocket.Conn, session *runnerpty.Session, done chan struct{}) {
	defer close(done)
	for {
		select {
		case chunk := <-session.Output():
			if err := writePTYFrame(connection, websocket.MessageBinary, chunk); err != nil {
				session.Cancel(runnerpty.TerminalCauseCancelled)
				drainUntilTerminal(session)
				return
			}
		case result := <-session.Terminal():
			_ = writePTYFrame(connection, websocket.MessageText, encodePTYTerminal(result))
			_ = connection.Close(websocket.StatusNormalClosure, "pty terminal")
			return
		}
	}
}

// writePTYFrame 以固定写超时发送一帧。
func writePTYFrame(connection *websocket.Conn, messageType websocket.MessageType, payload []byte) error {
	writeContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return connection.Write(writeContext, messageType, payload)
}

// drainUntilTerminal 消费剩余输出并等待终态，避免会话资源悬挂。
func drainUntilTerminal(session *runnerpty.Session) {
	for {
		select {
		case <-session.Output():
		case <-session.Terminal():
			return
		}
	}
}

// encodePTYTerminal 把会话终态映射为 terminal 文本事件。
func encodePTYTerminal(result runnerpty.TerminalResult) []byte {
	event := protocol.PTYServerEvent{
		Type:       protocol.PTYServerEventTerminal,
		ExitCode:   result.ExitCode,
		DurationMS: &result.DurationMS,
	}
	switch result.Cause {
	case runnerpty.TerminalCauseTimedOut:
		event.ErrorCode = "PTY_TIMED_OUT"
		event.Message = "PTY session timed out."
	case runnerpty.TerminalCauseCancelled:
		event.ErrorCode = "PTY_CANCELLED"
		event.Message = "PTY session was cancelled."
	}
	encoded, _ := json.Marshal(event)
	return encoded
}

// writePTYEvent 写出一条服务端文本事件。
func writePTYEvent(connection *websocket.Conn, event protocol.PTYServerEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return writePTYFrame(connection, websocket.MessageText, encoded)
}

// writePTYErrorEvent 在会话建立前报告错误事件并关闭连接。
func writePTYErrorEvent(connection *websocket.Conn, code, message string) {
	_ = writePTYEvent(connection, protocol.PTYServerEvent{
		Type:      protocol.PTYServerEventError,
		ErrorCode: code,
		Message:   message,
	})
	_ = connection.Close(websocket.StatusPolicyViolation, "pty error")
}

// newByteReader 供严格 JSON 解码复用。
func newByteReader(encoded []byte) io.Reader {
	return &byteReader{data: encoded}
}

type byteReader struct {
	data []byte
}

func (b *byteReader) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	count := copy(p, b.data)
	b.data = b.data[count:]
	return count, nil
}
