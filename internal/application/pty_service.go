package application

import (
	"context"
	"errors"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// PTYFrameConnection 是控制面 PTY 桥接的最小帧端口。
//
// 实现由 runner adapter 提供；接口方法与帧语义按结构化匹配，application
// 不依赖具体 WebSocket 库。
type PTYFrameConnection interface {
	// ReadFrame 阻塞读取下一帧；binary 为 false 表示文本帧。
	ReadFrame(ctx context.Context) (binary bool, payload []byte, err error)
	// WriteFrame 写出一帧。
	WriteFrame(ctx context.Context, binary bool, payload []byte) error
	// Close 关闭连接并触发对端会话取消。
	Close() error
}

// PTYDialerFactory 只允许按已通过 Store gate 的 sandbox ID 建立 PTY 连接。
type PTYDialerFactory interface {
	// DialPTY 与指定 sandbox 的 runner PTY endpoint 建立固定帧连接。
	DialPTY(ctx context.Context, sandboxID string) (PTYFrameConnection, error)
}

// PTYService 在 Store 生命周期 gate 后为调用方建立 PTY 桥接。
//
// 本服务不解释 PTY 消息语义；帧内容由 runner 与调用方直接协商，
// 控制面只在断开或错误时释放两侧连接。
type PTYService struct {
	store   store.Store
	factory PTYDialerFactory
}

// NewPTYService 创建 PTY 应用服务。
func NewPTYService(s store.Store, factory PTYDialerFactory) (*PTYService, error) {
	if s == nil || factory == nil {
		return nil, errors.New("pty service is not configured")
	}
	return &PTYService{store: s, factory: factory}, nil
}

// Connect 校验 sandbox 处于 Running 后返回 runner PTY 帧连接。
func (s *PTYService) Connect(ctx context.Context, sandboxID string) (PTYFrameConnection, error) {
	sandbox, err := s.store.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState != domain.StateRunning {
		return nil, domain.ErrSandboxNotRunning
	}
	connection, err := s.factory.DialPTY(ctx, sandboxID)
	if err != nil {
		return nil, mapPTYClientError(err)
	}
	return connection, nil
}

// mapPTYClientError 把 runner PTY 建立错误映射为稳定 domain 哨兵。
func mapPTYClientError(err error) error {
	if err == nil {
		return nil
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case string(protocol.ErrorCodePTYUnavailable):
			return domain.ErrPTYUnavailable
		case string(protocol.ErrorCodePTYLimitReached):
			return domain.ErrPTYLimitReached
		}
	}
	return domain.ErrRunnerUnhealthy
}
