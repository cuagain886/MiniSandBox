// Package api 实现 sandboxd 的 HTTP 适配层。
//
// 本模块负责请求解析、中间件、响应编码和错误映射；业务规则必须委托给
// application 层，不能直接操作 Docker 或 SQLite。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
)

// BuildInfo 描述当前 sandboxd 构建版本，用于健康检查和故障定位。
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// LifecycleService 定义生命周期 HTTP handler 允许调用的应用层用例。
//
// 接口刻意不暴露 Store、Runtime 或 reconcile 细节，避免 HTTP 层越过
// application 边界直接操作基础设施。
type LifecycleService interface {
	// CreateAccepted 原子提交创建意图并返回可直接写出的首次或 replay 响应。
	CreateAccepted(
		ctx context.Context,
		command application.CreateSandbox,
	) (application.IdempotentCreateOutcome, error)
	// Get 读取 sandbox 最近一次持久化的生命周期状态。
	Get(ctx context.Context, id string) (domain.Sandbox, error)
	// Delete 幂等提交 sandbox 终止意图；成功不等待资源清理完成。
	Delete(
		ctx context.Context,
		command application.DeleteSandbox,
	) (domain.Sandbox, error)
}

// RouterDependencies 保存 HTTP 适配层使用的应用服务。
//
// 零值用于尚未完成生产装配的初始化骨架，对应业务路由继续返回明确的
// NOT_IMPLEMENTED，而不是伪装成功。
type RouterDependencies struct {
	// Lifecycle 提供 sandbox 创建、查询和删除用例。
	Lifecycle LifecycleService
	// Execution 提供当前 sandbox 的命令创建、查询和取消用例。
	Execution ExecutionService
	// SSEWriteTimeout 是外部前台流每个 frame 的写出上限；零值使用安全默认值。
	SSEWriteTimeout time.Duration
	// Readiness 保存启动依赖的并发安全就绪状态；nil 等价于全部未就绪。
	Readiness *Readiness
}

// NewRouter 创建 sandboxd 的根 HTTP handler，并注册中间件与全部公开路由。
//
// dependencies 是可选单元素参数，用于保持初始化阶段调用方兼容；传入多个
// 依赖对象属于装配错误，会触发 panic。
func NewRouter(build BuildInfo, dependencies ...RouterDependencies) http.Handler {
	if len(dependencies) > 1 {
		panic("api: NewRouter accepts at most one RouterDependencies")
	}
	var deps RouterDependencies
	if len(dependencies) == 1 {
		deps = dependencies[0]
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "sandboxd",
			"build":   build,
		})
	})
	mux.HandleFunc("GET /readyz", readinessHandler(deps.Readiness))
	registerLifecycleRoutes(mux, deps.Lifecycle)
	registerExecutionRoutes(mux, deps.Execution, deps.SSEWriteTimeout)
	return requestIDMiddleware(mux)
}

// writeJSON 统一写入 JSON Content-Type、状态码和响应体。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
