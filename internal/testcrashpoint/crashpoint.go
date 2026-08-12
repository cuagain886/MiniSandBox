//go:build !integration

// Package testcrashpoint 为可靠性集成测试提供编译期隔离的崩溃边界。
// 生产构建只包含不可配置的 no-op；只有 integration build tag 才能连接测试 IPC。
package testcrashpoint

// Hit 在生产构建中始终立即返回，因此环境变量或配置无法启用故障注入。
func Hit(string) {}

// Drop 在生产构建中始终返回 false，不能丢弃正常控制流事件。
func Drop(string) bool { return false }
