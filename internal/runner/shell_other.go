//go:build !linux

package runner

// ResolveShell 在非 Linux 构建中 fail closed；production execution 仅支持 Linux。
func ResolveShell() (string, error) {
	return "", ErrShellNotFound
}
