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
// Phase 1 只接受镜像引用；资源、workspace、网络和平台全部来自服务端安全
// 默认值，不接受客户端宿主机路径或 Docker 配置。
type CreateSandbox struct {
	// Image 是用户请求的容器镜像引用，不能为空。
	Image string
}

// DeleteSandbox 表示将指定 sandbox 的期望状态设置为 Terminated。
type DeleteSandbox struct {
	SandboxID string
}

// Execute 表示在指定 sandbox 内提交一次命令执行。
type Execute struct {
	SandboxID string
	Spec      domain.ExecutionSpec
}
