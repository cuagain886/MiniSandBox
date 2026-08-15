package runnerclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"minisandbox/pkg/protocol"
)

// PTYFrameConnection 是 PTY WebSocket 的最小帧接口，供控制面桥接使用。
//
// 实现内部串行化写者，调用方可以在两个方向各用一个 goroutine 读写；
// Close 幂等并取消对端会话。
type PTYFrameConnection interface {
	// ReadFrame 阻塞读取下一帧；binary 为 false 表示文本帧。
	ReadFrame(ctx context.Context) (binary bool, payload []byte, err error)
	// WriteFrame 写出一帧。
	WriteFrame(ctx context.Context, binary bool, payload []byte) error
	// Close 关闭连接并释放底层资源。
	Close() error
}

// DialPTY 与当前 sandbox runner 的内部 PTY endpoint 建立固定连接。
//
// 目标路径与子协议都由本方法固定；调用方不能提供 URL、路径或 token。
// WebSocket 连接不受普通请求的短超时约束，复用同一 Unix Socket transport。
func (c *Client) DialPTY(ctx context.Context) (PTYFrameConnection, error) {
	if c == nil || c.authorization == nil || c.httpClient == nil {
		return nil, errors.New("runner client is not configured")
	}
	token, err := c.authorization()
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+string(token))
	dialClient := &http.Client{Transport: c.httpClient.Transport}
	connection, _, err := websocket.Dial(ctx, c.baseURL+"/v1/pty", &websocket.DialOptions{
		HTTPClient:   dialClient,
		Subprotocols: []string{protocol.PTYSubprotocol},
		HTTPHeader:   header,
	})
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) {
			return nil, status
		}
		return nil, &ConnectionError{cause: err}
	}
	if connection.Subprotocol() != protocol.PTYSubprotocol {
		_ = connection.Close(websocket.StatusProtocolError, "subprotocol mismatch")
		return nil, &ProtocolMismatchError{}
	}
	return &ptyFrameConnection{connection: connection}, nil
}

// ptyFrameConnection 把 *websocket.Conn 适配为帧接口并保护单写者约束。
type ptyFrameConnection struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
	closed     sync.Once
}

// ReadFrame 读取下一帧并把负载完整读入内存；帧大小由 readLimit 约束。
func (p *ptyFrameConnection) ReadFrame(ctx context.Context) (bool, []byte, error) {
	messageType, reader, err := p.connection.Reader(ctx)
	if err != nil {
		return false, nil, err
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return false, nil, err
	}
	return messageType == websocket.MessageBinary, payload, nil
}

// WriteFrame 串行写出一帧。
func (p *ptyFrameConnection) WriteFrame(ctx context.Context, binary bool, payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	messageType := websocket.MessageText
	if binary {
		messageType = websocket.MessageBinary
	}
	return p.connection.Write(ctx, messageType, payload)
}

// Close 幂等关闭连接；关闭即触发对端会话取消。
func (p *ptyFrameConnection) Close() error {
	var err error
	p.closed.Do(func() {
		err = p.connection.Close(websocket.StatusNormalClosure, "bridge closed")
	})
	return err
}
