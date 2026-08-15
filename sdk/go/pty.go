package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"minisandbox/pkg/protocol"
)

// PTYRequest 是打开交互终端的启动请求。
type PTYRequest = protocol.PTYStartRequest

// PTYTerminal 是会话唯一终态结果。
type PTYTerminal = protocol.PTYServerEvent

// PTYOutputChunk 是终端输出块；PTY 天生合并 stdout 与 stderr。
type PTYOutputChunk []byte

// OpenPTY 在当前 sandbox 中打开一个交互式 PTY。
//
// 连接使用 minisandbox.pty.v1 子协议；发送 start 后等待 started 事件
// 才返回。一条连接对应一个会话；Close 即取消，进程组按服务端语义终止。
func (s *Sandbox) OpenPTY(ctx context.Context, request PTYRequest) (*PTYConnection, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	connection, _, err := websocket.Dial(
		ctx,
		s.client.baseURL+"/v1/sandboxes/"+url.PathEscape(s.id)+"/pty",
		&websocket.DialOptions{
			Subprotocols: []string{protocol.PTYSubprotocol},
		},
	)
	if err != nil {
		return nil, err
	}
	if connection.Subprotocol() != protocol.PTYSubprotocol {
		_ = connection.Close(websocket.StatusProtocolError, "subprotocol mismatch")
		return nil, errors.New("minisandbox: PTY subprotocol mismatch")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		_ = connection.Close(websocket.StatusInternalError, "encode start")
		return nil, err
	}
	writeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := connection.Write(writeContext, websocket.MessageText, encoded); err != nil {
		_ = connection.Close(websocket.StatusInternalError, "write start")
		return nil, err
	}

	pty := &PTYConnection{
		connection: connection,
		output:     make(chan PTYOutputChunk, 64),
		terminal:   make(chan PTYTerminal, 1),
	}
	// 首个事件必须是 started；读取循环在后台持续消费。
	first, firstBinary, err := pty.readFrame(ctx)
	if err != nil {
		_ = connection.Close(websocket.StatusInternalError, "read started")
		return nil, err
	}
	if firstBinary {
		_ = connection.Close(websocket.StatusProtocolError, "expected started")
		return nil, errors.New("minisandbox: PTY first message must be started")
	}
	var started protocol.PTYServerEvent
	if err := json.Unmarshal(first, &started); err != nil || started.Type != protocol.PTYServerEventStarted {
		_ = connection.Close(websocket.StatusProtocolError, "invalid started")
		return nil, errors.New("minisandbox: PTY started event is invalid")
	}
	go pty.pump(ctx)
	return pty, nil
}

// PTYConnection 是一个已启动的交互式终端会话。
//
// Write 与 Resize 可并发调用；Output 与 Terminal 各自只投递一次序列。
type PTYConnection struct {
	connection *websocket.Conn

	output   chan PTYOutputChunk
	terminal chan PTYTerminal

	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

// Output 返回终端输出块通道；会话结束后通道关闭。
func (p *PTYConnection) Output() <-chan PTYOutputChunk { return p.output }

// Terminal 返回唯一终态事件通道。
func (p *PTYConnection) Terminal() <-chan PTYTerminal { return p.terminal }

// Write 向终端 stdin 写入字节；Ctrl-C 等控制字符按终端语义传递。
func (p *PTYConnection) Write(ctx context.Context, chunk []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.connection.Write(ctx, websocket.MessageBinary, chunk)
}

// Resize 调整终端窗口大小。
func (p *PTYConnection) Resize(ctx context.Context, cols, rows uint16) error {
	encoded, err := json.Marshal(protocol.PTYResizeRequest{Type: protocol.PTYMessageTypeResize, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.connection.Write(ctx, websocket.MessageText, encoded)
}

// Close 关闭会话；等价于取消，服务端终止整个进程组。
func (p *PTYConnection) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.connection.Close(websocket.StatusNormalClosure, "sdk closed")
}

// pump 在后台消费服务端帧并分发到对应通道。
func (p *PTYConnection) pump(ctx context.Context) {
	defer close(p.output)
	for {
		payload, binary, err := p.readFrame(ctx)
		if err != nil {
			return
		}
		if binary {
			select {
			case p.output <- PTYOutputChunk(payload):
			case <-ctx.Done():
				return
			}
			continue
		}
		var event protocol.PTYServerEvent
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		if event.Type == protocol.PTYServerEventTerminal {
			p.terminal <- event
			close(p.terminal)
			_ = p.Close()
			return
		}
	}
}

// readFrame 读取一帧并完整返回其负载。
func (p *PTYConnection) readFrame(ctx context.Context) ([]byte, bool, error) {
	messageType, reader, err := p.connection.Reader(ctx)
	if err != nil {
		return nil, false, err
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	return payload, messageType == websocket.MessageBinary, nil
}
