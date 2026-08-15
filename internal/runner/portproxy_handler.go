package runner

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"minisandbox/internal/runnerportproxy"
)

// portProxyMethods 是代理固定支持的 HTTP 方法集合；CONNECT、TRACE 与
// Upgrade 语义一律拒绝。
var portProxyMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodHead:    true,
	http.MethodOptions: false,
}

// NewPortProxyHandler 返回 runner 内部固定路由的 HTTP 代理 handler。
//
// 路由参数 port 与深通配 path 由 mux 解析；请求头先剥离 runner 认证与
// hop-by-hop 头，再转发到 127.0.0.1:port。响应按到达顺序流式回写。
func NewPortProxyHandler(service *runnerportproxy.Service) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("port proxy service is not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !portProxyMethods[r.Method] {
			writeRunnerError(w, http.StatusMethodNotAllowed, "PORT_PROXY_UNAVAILABLE", "proxy method is not supported", false)
			return
		}
		port, err := strconv.Atoi(r.PathValue("port"))
		if err != nil {
			writeRunnerError(w, http.StatusBadRequest, "INVALID_PORT", "proxy port is invalid", false)
			return
		}
		pathAndQuery := "/" + r.PathValue("path")
		if r.URL.RawQuery != "" {
			pathAndQuery += "?" + r.URL.RawQuery
		}
		// 请求体原样流转发；context 取消同时终止两侧。
		response, err := service.Forward(r.Context(), port, r.Method, pathAndQuery, r.Header, r.Body)
		if err != nil {
			if errors.Is(err, runnerportproxy.ErrInvalidPort) {
				writeRunnerError(w, http.StatusBadRequest, "INVALID_PORT", "proxy port is invalid", false)
				return
			}
			writeRunnerError(w, http.StatusBadGateway, "PORT_UPSTREAM_UNAVAILABLE", "sandbox loopback service is unavailable", true)
			return
		}
		defer response.Body.Close()

		for name, values := range runnerportproxy.SanitizeHeaders(response.Header) {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		// 标记头供控制面区分上游业务状态与代理基础设施错误；对外前剥离。
		w.Header().Set("X-MiniSandbox-Proxied", "v1")
		w.WriteHeader(response.StatusCode)
		if r.Method == http.MethodHead {
			return
		}
		// 上游读取或客户端写出失败都立即结束；内容不经过内存整体缓冲。
		_, _ = io.Copy(w, response.Body)
	}), nil
}
