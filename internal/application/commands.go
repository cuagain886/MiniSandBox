// Package application 编排 sandbox 生命周期和命令执行用例。
//
// 本模块连接领域模型、持久化端口和 runtime 端口，负责提交期望状态和执行权限
// 校验，但不包含 HTTP、Docker 或 SQLite 的具体实现。
package application

import (
	"time"

	"minisandbox/internal/domain"
)

// CreateSandbox 表示创建 sandbox 的应用层命令。
//
// RequestKey 用于请求重试时的幂等去重，TTL 为零时由配置层提供默认值。
type CreateSandbox struct {
	Image      string
	Command    []string
	Env        map[string]string
	TTL        time.Duration
	RequestKey string
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
