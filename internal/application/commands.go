// Package application 编排 sandbox 生命周期和命令执行用例。
//
// 本模块连接领域模型、持久化端口和 runtime 端口，负责提交期望状态和执行权限
// 校验，但不包含 HTTP、Docker 或 SQLite 的具体实现。
package application

import (
	"minisandbox/internal/domain"
)

// CreateSandbox 表示创建 sandbox 的应用层命令。
//
// Phase 2 除镜像外只接受 outbound 布尔意图；资源、workspace、平台以及任何
// Docker/sidecar 细节仍全部来自服务端安全默认值。
type CreateSandbox struct {
	// Image 是用户请求的容器镜像引用，不能为空。
	Image string
	// Outbound 表示用户是否请求受管公网出站能力；缺失公共字段时为 false。
	Outbound bool
	// TTLSeconds 是客户端显式提供的整秒 TTL；nil 保留“字段缺失”的幂等身份。
	TTLSeconds *int64
	// Idempotency 是可选、已校验且已作用域化的创建重放身份。
	Idempotency *IdempotencyKey
}

// DeleteSandbox 表示将指定 sandbox 的期望状态设置为 Terminated。
type DeleteSandbox struct {
	SandboxID string
}

// Execute 表示在指定 sandbox 内提交一次命令执行。
type Execute struct {
	// SandboxID 是 Store gate 和 runner client factory 共用的唯一 sandbox 标识。
	SandboxID string
	// Spec 是不被 application 解释或重写的用户命令规格。
	Spec domain.ExecutionSpec
	// Background 决定返回 typed stream 还是后台 descriptor。
	Background bool
}
