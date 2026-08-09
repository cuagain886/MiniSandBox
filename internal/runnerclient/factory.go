package runnerclient

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
)

const runnerShutdownTimeout = 5 * time.Second

// AuthenticationError 表示 runner 拒绝了派生凭据；错误文本不包含 token。
type AuthenticationError struct{}

// Error 返回稳定且不可泄密的认证错误文本。
func (*AuthenticationError) Error() string { return "runner authentication failed" }

// ConnectionError 表示 health gate 无法确认 runner 可用。
type ConnectionError struct{ cause error }

// Error 返回不包含 socket path、响应正文或 token 的固定文本。
func (*ConnectionError) Error() string { return "runner connection failed" }

// Unwrap 返回底层分类，供调用方判断 context cancellation 等原因。
func (e *ConnectionError) Unwrap() error { return e.cause }

// Factory 按 sandbox ID 绑定固定 Unix Socket，并在每次发送时派生认证 token。
type Factory struct {
	mu                      sync.RWMutex
	socketRoot              string
	masterKey               runnerauth.MasterKey
	expectedProtocolVersion int
	healthCacheTTL          time.Duration
	closed                  bool
}

// NewFactory 创建 runner client factory；master key 被私有复制并由 Close 清零。
func NewFactory(socketRoot string, masterKey *runnerauth.MasterKey, expectedProtocolVersion int, healthCacheTTL time.Duration) (*Factory, error) {
	if !filepath.IsAbs(socketRoot) || masterKey == nil || expectedProtocolVersion <= 0 || healthCacheTTL <= 0 {
		return nil, errors.New("runner client factory is not configured")
	}
	probe, err := runnerauth.DeriveToken(masterKey, "00010203-0405-4607-8809-0a0b0c0d0e0f")
	if err != nil {
		return nil, errors.New("runner client factory master key is invalid")
	}
	probe.Clear()
	return &Factory{socketRoot: filepath.Clean(socketRoot), masterKey: *masterKey, expectedProtocolVersion: expectedProtocolVersion, healthCacheTTL: healthCacheTTL}, nil
}

// Client 创建只绑定给一个规范 sandbox ID 的 client。
func (f *Factory) Client(sandboxID string) (*Client, error) {
	if f == nil || !validSandboxID(sandboxID) {
		return nil, errors.New("runner client sandbox ID is invalid")
	}
	f.mu.RLock()
	if f.closed {
		f.mu.RUnlock()
		return nil, errors.New("runner client factory is closed")
	}
	socketRoot := f.socketRoot
	expected := f.expectedProtocolVersion
	cacheTTL := f.healthCacheTTL
	f.mu.RUnlock()
	directory := filepath.Join(socketRoot, sandboxID)
	relative, err := filepath.Rel(socketRoot, directory)
	if err != nil || relative != sandboxID || filepath.IsAbs(relative) || strings.Contains(relative, string(filepath.Separator)) {
		return nil, errors.New("runner client socket escapes managed root")
	}
	client := &Client{
		httpClient:              &http.Client{Transport: unixTransport(filepath.Join(directory, runnerSocketName))},
		baseURL:                 "http://runner",
		expectedProtocolVersion: expected,
		healthCacheTTL:          cacheTTL,
		now:                     time.Now,
	}
	client.authorization = func() ([]byte, error) { return f.authorization(sandboxID) }
	return client, nil
}

// Shutdown 通过固定 Unix Socket 端点有界关闭指定 sandbox 的 runner 准入与全部 execution。
func (f *Factory) Shutdown(ctx context.Context, sandboxID string) error {
	client, err := f.Client(sandboxID)
	if err != nil {
		return err
	}
	shutdownContext, cancel := context.WithTimeout(ctx, runnerShutdownTimeout)
	defer cancel()
	return client.Shutdown(shutdownContext)
}

// Probe 对固定 sandbox client 执行有界 health 检查，并精确校验调用方声明的协议版本。
func (f *Factory) Probe(ctx context.Context, sandboxID string, expectedProtocolVersion int) error {
	_, err := f.ProbeNetwork(ctx, sandboxID, expectedProtocolVersion)
	return err
}

// ProbeNetwork 执行有界 health 检查并返回经过严格格式校验的 runner netns identity。
func (f *Factory) ProbeNetwork(ctx context.Context, sandboxID string, expectedProtocolVersion int) (string, error) {
	if expectedProtocolVersion != runnerbootstrap.CurrentProtocolVersion {
		return "", &ProtocolMismatchError{}
	}
	client, err := f.Client(sandboxID)
	if err != nil {
		return "", err
	}
	f.mu.RLock()
	timeout := f.healthCacheTTL
	f.mu.RUnlock()
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		health, err := client.Health(probeContext, expectedProtocolVersion)
		if err == nil {
			return health.NetNSIdentity, nil
		}
		if !isRetryableConnectError(err) {
			return "", err
		}
		timer := time.NewTimer(runnerProbeRetryInterval)
		select {
		case <-probeContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return "", probeContextError(probeContext.Err(), err)
		case <-timer.C:
		}
	}
}

func (f *Factory) authorization(sandboxID string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, errors.New("runner client factory is closed")
	}
	token, err := runnerauth.DeriveToken(&f.masterKey, sandboxID)
	if err != nil {
		return nil, errors.New("derive runner authorization failed")
	}
	defer token.Clear()
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(token)))
	base64.RawURLEncoding.Encode(encoded, token[:])
	return encoded, nil
}

// Close 清零 factory 持有的主密钥副本；既有 client 后续请求会明确失败。
func (f *Factory) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if !f.closed {
		f.masterKey.Clear()
		f.closed = true
	}
	f.mu.Unlock()
}

// String 返回不包含主密钥、token 或 socket root 的固定诊断文本。
func (*Factory) String() string { return "runnerclient.Factory{redacted}" }

// GoString 返回不包含主密钥、token 或 socket root 的固定 Go 格式文本。
func (*Factory) GoString() string { return "runnerclient.Factory{redacted}" }
