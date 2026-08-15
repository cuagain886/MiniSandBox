package api

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	"minisandbox/internal/runnerportproxy"
)

// PortProxyService 定义公共端口代理 handler 允许调用的应用层用例。
type PortProxyService interface {
	// Forward 校验 sandbox 与端口后转发请求并返回上游响应。
	Forward(ctx context.Context, sandboxID string, port int, method, pathAndQuery string, header http.Header, body io.Reader) (*http.Response, error)
}

// NewSandboxPortProxyHandler 返回公共端口代理 handler。
//
// 请求头在转发前删除控制面认证与 hop-by-hop 头；上游状态码、头与体按
// 到达顺序回写，内部代理标记头对外剥离。
func NewSandboxPortProxyHandler(service PortProxyService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		port, err := strconv.Atoi(r.PathValue("port"))
		if err != nil {
			writeError(w, r, domain.ErrInvalidPort)
			return
		}
		pathAndQuery := "/" + r.PathValue("path")
		if r.URL.RawQuery != "" {
			pathAndQuery += "?" + r.URL.RawQuery
		}
		response, err := service.Forward(
			r.Context(), sandboxID, port, r.Method, pathAndQuery,
			runnerportproxy.SanitizeHeaders(r.Header), r.Body,
		)
		if err != nil {
			writeError(w, r, err)
			return
		}
		defer response.Body.Close()
		for name, values := range runnerportproxy.SanitizeHeaders(response.Header) {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Del(runnerclient.ProxiedResponseHeader)
		w.WriteHeader(response.StatusCode)
		if r.Method == http.MethodHead {
			return
		}
		// 上游读取或客户端写出失败立即结束；内容不整体缓冲。
		_, _ = io.Copy(w, response.Body)
	})
}
