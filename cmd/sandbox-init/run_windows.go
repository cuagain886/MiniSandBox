//go:build windows

package main

import "log/slog"

// run 在 Windows 上返回明确错误；sandbox-init 只会被注入 Linux 容器。
func run([]string) int {
	slog.Error("sandbox-init is supported only in Linux sandbox containers")
	return 2
}
