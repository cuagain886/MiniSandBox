// Package runner 实现 runnerd 的容器内命令执行服务。
//
// 本模块设计上负责鉴权、进程组、输出流、取消和后台任务；当前初始化骨架仅实现
// 鉴权、服务生命周期及部分基础结构。它只能影响当前 sandbox，不得依赖 Docker
// SDK、控制面 service 或持久化 store。
package runner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// NewServer 创建 runnerd 内部 HTTP handler。
//
// handler 只暴露当前 sandbox 的健康检查和执行资源，不接受 sandbox ID。
func NewServer(version, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "runnerd",
			"version": version,
		})
	})
	mux.HandleFunc("POST /v1/executions", notImplemented)
	mux.HandleFunc("DELETE /v1/executions/{execution_id}", notImplemented)
	return TokenAuth(token, mux)
}

// Serve 在给定 listener 上运行 runner HTTP 服务，并在 context 取消时优雅关闭。
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// context 通常由容器终止信号触发。先优雅关闭 HTTP 服务，让进行中的 handler
	// 有机会把最终退出事件写完，再由上层关闭 Unix Socket。
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()
			_ = server.Shutdown(shutdownContext)
		case <-stopped:
		}
	}()

	err := server.Serve(listener)
	close(stopped)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    "not_implemented",
		"message": "runner execution is not implemented in the initialization scaffold",
	})
}
