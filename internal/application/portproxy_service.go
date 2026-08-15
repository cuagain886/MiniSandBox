package application

import (
	"context"
	"errors"
	"io"
	"net/http"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
	"minisandbox/pkg/protocol"
)

// PortProxyClient 是 application 调用 runner 端口代理的固定端口。
type PortProxyClient interface {
	// Proxy 转发一次 HTTP 请求；成功响应带代理标记头，Body 由调用方关闭。
	Proxy(ctx context.Context, port int, method, pathAndQuery string, header http.Header, body io.Reader) (*http.Response, error)
}

// PortProxyClientFactory 只允许按已通过 Store gate 的 sandbox ID 选择代理 client。
type PortProxyClientFactory interface {
	// Client 返回绑定到指定 sandbox 的固定 client。
	Client(sandboxID string) (PortProxyClient, error)
}

// PortProxyService 在 Store 生命周期 gate 后转发 sandbox loopback HTTP。
//
// 端口范围由服务端配置固定；目标 host、scheme 与 socket 都不由调用方
// 决定。请求头在进入 runner 前删除控制面认证信息。
type PortProxyService struct {
	store   store.Store
	factory PortProxyClientFactory
	minimum int
	maximum int
}

// NewPortProxyService 创建端口代理应用服务。
func NewPortProxyService(s store.Store, factory PortProxyClientFactory, minimum, maximum int) (*PortProxyService, error) {
	if s == nil || factory == nil {
		return nil, errors.New("port proxy service is not configured")
	}
	if minimum < 1 || maximum > 65535 || minimum > maximum {
		return nil, errors.New("port proxy range is invalid")
	}
	return &PortProxyService{store: s, factory: factory, minimum: minimum, maximum: maximum}, nil
}

// Forward 校验端口与 sandbox 状态后转发请求。
func (s *PortProxyService) Forward(
	ctx context.Context,
	sandboxID string,
	port int,
	method string,
	pathAndQuery string,
	header http.Header,
	body io.Reader,
) (*http.Response, error) {
	if port < s.minimum || port > s.maximum {
		return nil, domain.ErrInvalidPort
	}
	sandbox, err := s.store.Get(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState != domain.StateRunning {
		return nil, domain.ErrSandboxNotRunning
	}
	client, err := s.factory.Client(sandboxID)
	if err != nil || client == nil {
		return nil, domain.ErrRunnerUnhealthy
	}
	response, err := client.Proxy(ctx, port, method, pathAndQuery, header, body)
	if err != nil {
		return nil, mapPortProxyClientError(err)
	}
	if response == nil {
		return nil, domain.ErrRunnerUnhealthy
	}
	return response, nil
}

// mapPortProxyClientError 把 runner 代理错误映射为稳定 domain 哨兵。
func mapPortProxyClientError(err error) error {
	if err == nil {
		return nil
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case string(protocol.ErrorCodeInvalidPort):
			return domain.ErrInvalidPort
		case string(protocol.ErrorCodePortProxyUnavailable):
			return domain.ErrPortProxyUnavailable
		case string(protocol.ErrorCodePortUpstreamUnavailable):
			return domain.ErrPortUpstreamUnavailable
		}
	}
	return domain.ErrRunnerUnhealthy
}
