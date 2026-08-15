// Package runnerportproxy 实现 runner 内的 sandbox loopback HTTP 代理。
//
// 目标固定为当前 sandbox 网络命名空间中的 127.0.0.1:port；调用方不能
// 提供 host、scheme 或 socket。请求与响应都剥离控制面认证和 hop-by-hop
// 头，内容按流转发。
package runnerportproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidPort 表示端口不在允许范围内。
var ErrInvalidPort = errors.New("invalid proxy port")

// ErrUnsupported 表示代理能力未启用。
var ErrUnsupported = errors.New("port proxy unavailable")

// hopByHopAndControlHeaders 是转发前必须删除的请求头集合。
//
// Authorization 与 X-Forwarded-* 是控制面身份信息，进入 sandbox 应用属于
// 凭据泄漏；其余为 hop-by-hop 头，不允许跨跃点转发。
var hopByHopAndControlHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
}

// Service 是 loopback HTTP 代理服务。
type Service struct {
	transport *http.Transport
	minimum   int
	maximum   int
}

// NewService 创建代理服务并固定端口范围。
//
// Transport 显式禁用环境代理与重定向，拨号目标由代码构造为
// 127.0.0.1:port；keep-alive 关闭以保证删除与超时语义简单可控。
func NewService(minimum, maximum int) (*Service, error) {
	if minimum < 1 || maximum > 65535 || minimum > maximum {
		return nil, ErrInvalidPort
	}
	return &Service{
		transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			// addr 由本服务构造的 127.0.0.1:port 请求推导，不经调用方。
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := &net.Dialer{Timeout: 5 * time.Second}
				return dialer.DialContext(ctx, network, addr)
			},
		},
		minimum: minimum,
		maximum: maximum,
	}, nil
}

// Close 释放传输层资源。
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.transport.CloseIdleConnections()
	return nil
}

// Forward 构造并执行一次 loopback HTTP 请求。
//
// port 必须在服务范围内；pathAndQuery 以 "/" 开头并包含调用方查询串。
// 请求头先经 SanitizeHeaders 清洗，Host 固定为 127.0.0.1:port。
// 响应由调用方负责关闭 Body。
func (s *Service) Forward(
	ctx context.Context,
	port int,
	method string,
	pathAndQuery string,
	header http.Header,
	body io.Reader,
) (*http.Response, error) {
	if s == nil {
		return nil, ErrUnsupported
	}
	if port < s.minimum || port > s.maximum {
		return nil, ErrInvalidPort
	}
	if !strings.HasPrefix(pathAndQuery, "/") {
		return nil, errors.New("proxy path must start with /")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://127.0.0.1:"+strconv.Itoa(port)+pathAndQuery,
		body,
	)
	if err != nil {
		return nil, err
	}
	for name, values := range SanitizeHeaders(header) {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Host = "127.0.0.1:" + strconv.Itoa(port)
	// RoundTrip 不跟随重定向；3xx 响应原样返回给调用方决定。
	response, err := s.transport.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// SanitizeHeaders 返回删除控制面与 hop-by-hop 头后的副本。
func SanitizeHeaders(header http.Header) http.Header {
	sanitized := http.Header{}
	// Connection 头列出的动态 hop-by-hop 头也必须删除。
	connectionTokens := map[string]bool{}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			connectionTokens[strings.ToLower(strings.TrimSpace(token))] = true
		}
	}
	for name, values := range header {
		lower := strings.ToLower(name)
		if connectionTokens[lower] || strings.HasPrefix(lower, "x-minisandbox-") {
			continue
		}
		if forbiddenHeader(lower) {
			continue
		}
		for _, value := range values {
			sanitized.Add(name, value)
		}
	}
	return sanitized
}

// forbiddenHeader 报告名称是否在固定删除集合中。
func forbiddenHeader(lowerName string) bool {
	for _, candidate := range hopByHopAndControlHeaders {
		if strings.ToLower(candidate) == lowerName {
			return true
		}
	}
	return false
}
