package api

import (
	"context"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// ptyBridgeFrameLimit 是桥接单帧的字节上限，与 runner 侧控制帧上限一致。
const ptyBridgeFrameLimit = 1 << 20

// PTYService 定义公共 PTY handler 允许调用的应用层用例。
type PTYService interface {
	// Connect 校验 sandbox 后返回 runner PTY 帧连接。
	Connect(ctx context.Context, sandboxID string) (application.PTYFrameConnection, error)
}

// NewSandboxPTYHandler 返回公共 PTY WebSocket handler。
//
// 外部连接固定使用 minisandbox.pty.v1 子协议；建立后按帧原样桥接到
// 当前 sandbox 的 runner PTY 连接，两个方向各一个读写 goroutine。任一
// 侧断开都会关闭另一侧，由 runner 终止进程组。
func NewSandboxPTYHandler(service PTYService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{protocol.PTYSubprotocol},
		})
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusInternalError, "sandboxd pty closed")
		if connection.Subprotocol() != protocol.PTYSubprotocol {
			_ = connection.Close(websocket.StatusPolicyViolation, "subprotocol required")
			return
		}
		upstream, err := service.Connect(r.Context(), sandboxID)
		if err != nil {
			// 升级已完成，只能用关闭码表达失败原因。
			_ = connection.Close(websocket.StatusPolicyViolation, "pty unavailable")
			return
		}
		defer upstream.Close()

		bridgeContext, cancel := context.WithCancel(r.Context())
		defer cancel()
		done := make(chan struct{})
		var once sync.Once
		finish := func() { once.Do(func() { cancel(); close(done) }) }

		go func() {
			bridgePTYFrames(bridgeContext, upstream, connection)
			finish()
		}()
		bridgePTYFramesWebSocket(bridgeContext, connection, upstream)
		finish()
	})
}

// bridgePTYFrames 把 runner 帧转发给外部连接。
func bridgePTYFrames(ctx context.Context, upstream application.PTYFrameConnection, external *websocket.Conn) {
	for {
		binary, payload, err := upstream.ReadFrame(ctx)
		if err != nil {
			return
		}
		messageType := websocket.MessageText
		if binary {
			messageType = websocket.MessageBinary
		}
		if err := external.Write(ctx, messageType, payload); err != nil {
			return
		}
	}
}

// bridgePTYFramesWebSocket 把外部帧转发给 runner 连接。
//
// 帧大小受 SetReadLimit 约束；帧内按块累积后整体转发。
func bridgePTYFramesWebSocket(ctx context.Context, external *websocket.Conn, upstream application.PTYFrameConnection) {
	external.SetReadLimit(ptyBridgeFrameLimit)
	for {
		messageType, reader, err := external.Reader(ctx)
		if err != nil {
			return
		}
		payload := make([]byte, 0, 4096)
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := reader.Read(buffer)
			payload = append(payload, buffer[:read]...)
			if readErr != nil {
				break
			}
		}
		if err := upstream.WriteFrame(ctx, messageType == websocket.MessageBinary, payload); err != nil {
			return
		}
	}
}
