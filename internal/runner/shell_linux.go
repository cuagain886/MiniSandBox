//go:build linux

package runner

import (
	"os"
	"syscall"
)

var fixedShellCandidates = []string{"/bin/bash", "/bin/sh"}

type shellFileOps struct {
	stat   func(string) (os.FileInfo, error)
	access func(string, uint32) error
}

var osShellFileOps = shellFileOps{
	stat: os.Stat,
	access: func(path string, mode uint32) error {
		return syscall.Access(path, mode)
	},
}

// ResolveShell 按 `/bin/bash`、`/bin/sh` 的固定顺序选择当前 execution 身份可执行的 regular file。
// 本函数不读取 SHELL、PATH 或其他环境变量。
func ResolveShell() (string, error) {
	return resolveShell(fixedShellCandidates, osShellFileOps)
}

func resolveShell(candidates []string, ops shellFileOps) (string, error) {
	for _, path := range candidates {
		info, err := ops.stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := ops.access(path, 1); err != nil { // X_OK 使用调用进程的实际 UID/GID 判定可执行性。
			continue
		}
		return path, nil
	}
	return "", ErrShellNotFound
}
