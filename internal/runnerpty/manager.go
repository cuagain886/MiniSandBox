package runnerpty

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Manager 管理当前 sandbox 的全部 PTY 会话。
//
// Manager 只在 runner 进程内存在；不暴露列表或查询接口，关闭时统一
// 取消全部会话并等待有界收尾。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	limit    int
}

// NewManager 创建并发上限受限的 PTY 会话管理器。
func NewManager(maxSessions int) (*Manager, error) {
	if maxSessions <= 0 {
		return nil, errors.New("PTY manager session limit is invalid")
	}
	return &Manager{sessions: make(map[string]*Session), limit: maxSessions}, nil
}

// Start 创建并注册一个新会话。
//
// 平台不支持或会话已满时立即失败；进程启动失败会在返回前撤销注册。
func (m *Manager) Start(parent context.Context, options StartOptions) (*Session, error) {
	if m == nil || parent == nil {
		return nil, errors.New("PTY manager is not configured")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	session := &Session{
		manager:   m,
		id:        newSessionID(),
		ctx:       ctx,
		cancel:    cancel,
		output:    make(chan []byte, 64),
		terminal:  make(chan TerminalResult, 1),
		done:      make(chan struct{}),
		startedAt: time.Now(),
	}
	if !m.register(session) {
		cancel()
		return nil, ErrLimitReached
	}
	process, err := spawnPTYProcess(options)
	if err != nil {
		m.remove(session)
		cancel()
		return nil, err
	}
	session.process = process
	go session.supervise(options)
	go session.pumpOutput()
	return session, nil
}

// Shutdown 取消全部会话并等待有界收尾；重复调用安全。
func (m *Manager) Shutdown(grace time.Duration) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.Cancel(TerminalCauseCancelled)
	}
	deadline := time.Now().Add(grace)
	for _, session := range sessions {
		select {
		case <-session.Terminal():
		case <-time.After(time.Until(deadline)):
			return
		}
	}
}

// register 在容量允许时注册会话并报告是否成功。
func (m *Manager) register(session *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= m.limit {
		return false
	}
	m.sessions[session.id] = session
	return true
}

func (m *Manager) remove(session *Session) {
	m.removeByID(session.id)
}

func (m *Manager) removeByID(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// newSessionID 生成不可预测的会话标识。
func newSessionID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "pty-unknown"
	}
	return hex.EncodeToString(random[:])
}
