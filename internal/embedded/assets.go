// Package embedded 管理注入 sandbox 容器的静态 Linux 二进制。
//
// runnerd 和 sandbox-init 由构建流程生成后嵌入 sandboxd；本模块只负责读取
// 构建产物，不负责生成或执行这些二进制。
package embedded

import "embed"

// artifacts 在 sandboxd 构建前由静态 Linux runnerd 和 sandbox-init 二进制替换。
//
//go:embed artifacts/linux_amd64/*
var artifacts embed.FS

// ReadLinuxAMD64 读取准备注入 Linux amd64 sandbox 的指定构建产物。
func ReadLinuxAMD64(name string) ([]byte, error) {
	return artifacts.ReadFile("artifacts/linux_amd64/" + name)
}
