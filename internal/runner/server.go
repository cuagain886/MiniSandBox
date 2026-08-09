// Package runner 实现 runnerd 的容器内命令执行服务。
//
// 本模块负责当前 sandbox 的鉴权、进程组、输出、取消和后台任务；它不能访问
// Docker socket、控制面 service 或其他 sandbox 的资源。
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

const runnerShutdownTimeout = 5 * time.Second

// ServerRoutes 是 runner 显式允许的 execution 路由集合。
// 字段必须全部配置；组合层不能用 catch-all handler 代理任意内部路径。
type ServerRoutes struct {
	// Create 处理 POST /v1/executions。
	Create http.Handler
	// Status 处理 GET /v1/executions/{execution_id}。
	Status http.Handler
	// Cancel 处理 DELETE /v1/executions/{execution_id}。
	Cancel http.Handler
	// Logs 处理 GET /v1/executions/{execution_id}/logs。
	Logs http.Handler
	// Shutdown 处理 POST /v1/shutdown，并永久关闭当前 sandbox 的 execution 准入。
	Shutdown http.Handler
}

// ServerReadiness 保存 runner 的单向 readiness 状态；开始 draining 后不可恢复。
type ServerReadiness struct {
	ready    atomic.Bool
	draining atomic.Bool
}

// NewServerReadiness 创建尚未 ready 的启动状态。
func NewServerReadiness() *ServerReadiness { return &ServerReadiness{} }

// MarkReady 仅在 bootstrap、降权和身份验证全部成功后开放 readiness。
func (s *ServerReadiness) MarkReady() error {
	if s == nil || s.draining.Load() {
		return errors.New("runner readiness cannot become ready")
	}
	s.ready.Store(true)
	return nil
}

// StartDraining 永久关闭 readiness，并使 health 显式报告 draining。
func (s *ServerReadiness) StartDraining() {
	if s == nil {
		return
	}
	s.draining.Store(true)
	s.ready.Store(false)
}

// NewServer 创建兼容初始化路径使用的 runner handler。
//
// 完整 composition root 应使用 NewConfiguredServer 注入五个固定操作；本入口保留给
// 尚未接入 Phase 2 bootstrap 的调用方，并明确拒绝 execution 操作。
func NewServer(version, token string) (http.Handler, error) {
	readiness := NewServerReadiness()
	if err := readiness.MarkReady(); err != nil {
		return nil, err
	}
	routes := ServerRoutes{Create: http.HandlerFunc(notImplemented), Status: http.HandlerFunc(notImplemented), Cancel: http.HandlerFunc(notImplemented), Logs: http.HandlerFunc(notImplemented), Shutdown: http.HandlerFunc(notImplemented)}
	return newConfiguredServer(version, token, readiness, routes, currentNetNSIdentity)
}

// NewConfiguredServer 创建只暴露 health 与四类固定 execution route 的完整 handler。
func NewConfiguredServer(version, token string, readiness *ServerReadiness, routes ServerRoutes) (http.Handler, error) {
	return newConfiguredServer(version, token, readiness, routes, currentNetNSIdentity)
}

func newConfiguredServer(version, token string, readiness *ServerReadiness, routes ServerRoutes, readNetNSIdentity func() (string, error)) (http.Handler, error) {
	if version == "" || readiness == nil || readNetNSIdentity == nil {
		return nil, errors.New("runner server identity is not configured")
	}
	if routes.Create == nil || routes.Status == nil || routes.Cancel == nil || routes.Logs == nil || routes.Shutdown == nil {
		return nil, errors.New("runner server routes are not configured")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !readiness.ready.Load() {
			status := "starting"
			if readiness.draining.Load() {
				status = "draining"
			}
			writeHealth(w, version, status, "")
			return
		}
		identity, err := readNetNSIdentity()
		if err != nil {
			writeRunnerError(w, http.StatusServiceUnavailable, "NETNS_IDENTITY_UNAVAILABLE", "runner network namespace identity is unavailable", true)
			return
		}
		writeHealth(w, version, "ok", identity)
	})
	mux.Handle("POST /v1/executions", routes.Create)
	mux.Handle("GET /v1/executions/{execution_id}", routes.Status)
	mux.Handle("DELETE /v1/executions/{execution_id}", routes.Cancel)
	mux.Handle("GET /v1/executions/{execution_id}/logs", routes.Logs)
	mux.Handle("POST /v1/shutdown", routes.Shutdown)
	authenticated, err := TokenAuth(token, mux)
	if err != nil {
		return nil, err
	}
	return RunnerRequestPolicy(authenticated)
}

// newServer 保留 netns reader 注入点，供 health contract 测试使用。
func newServer(version, token string, readNetNSIdentity func() (string, error)) (http.Handler, error) {
	readiness := NewServerReadiness()
	if err := readiness.MarkReady(); err != nil {
		return nil, err
	}
	routes := ServerRoutes{Create: http.HandlerFunc(notImplemented), Status: http.HandlerFunc(notImplemented), Cancel: http.HandlerFunc(notImplemented), Logs: http.HandlerFunc(notImplemented), Shutdown: http.HandlerFunc(notImplemented)}
	return newConfiguredServer(version, token, readiness, routes, readNetNSIdentity)
}

func writeHealth(w http.ResponseWriter, version, status, identity string) {
	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(protocol.RunnerHealth{Status: status, Service: "runnerd", Version: version, ProtocolVersion: runnerbootstrap.CurrentProtocolVersion, NetNSIdentity: identity})
}

// Serve 在给定 listener 上运行 runner HTTP 服务，并在 context 取消时优雅关闭。
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	return serveManaged(ctx, listener, handler, nil, nil)
}

// ServeManaged 先关闭 execution 准入并收敛 Manager，再关闭 HTTP server。
func ServeManaged(ctx context.Context, listener net.Listener, handler http.Handler, manager *Manager, readiness *ServerReadiness) error {
	if manager == nil || readiness == nil {
		return errors.New("runner managed server is not configured")
	}
	return serveManaged(ctx, listener, handler, manager, readiness)
}

func serveManaged(ctx context.Context, listener net.Listener, handler http.Handler, manager *Manager, readiness *ServerReadiness) error {
	if ctx == nil || listener == nil || handler == nil {
		return errors.New("runner HTTP server is not configured")
	}
	server := newRunnerHTTPServer(handler)
	shutdownResult := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if readiness != nil {
				readiness.StartDraining()
			}
			shutdownContext, cancel := context.WithTimeout(context.Background(), runnerShutdownTimeout)
			defer cancel()
			if manager != nil {
				if err := manager.Shutdown(shutdownContext); err != nil {
					shutdownResult <- err
					_ = server.Close()
					return
				}
			}
			shutdownResult <- server.Shutdown(shutdownContext)
		case <-stopped:
		}
	}()
	err := server.Serve(listener)
	close(stopped)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	select {
	case shutdownErr := <-shutdownResult:
		return shutdownErr
	default:
		return nil
	}
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeRunnerError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "runner operation is not implemented", false)
}
