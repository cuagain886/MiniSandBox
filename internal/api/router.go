// Package api 实现 sandboxd 的 HTTP 适配层。
//
// 本模块负责请求解析、中间件、响应编码和错误映射；业务规则必须委托给
// application 层，不能直接操作 Docker 或 SQLite。
package api

import (
	"encoding/json"
	"net/http"
)

// BuildInfo 描述当前 sandboxd 构建版本，用于健康检查和故障定位。
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// NewRouter 创建 sandboxd 的根 HTTP handler，并注册中间件与全部公开路由。
func NewRouter(build BuildInfo) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "sandboxd",
			"build":   build,
		})
	})
	registerLifecycleRoutes(mux)
	registerExecutionRoutes(mux)
	return requestIDMiddleware(mux)
}

// writeJSON 统一写入 JSON Content-Type、状态码和响应体。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
