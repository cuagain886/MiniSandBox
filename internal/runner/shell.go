package runner

import "errors"

// ErrShellNotFound 表示固定候选中没有可执行 regular file，对应稳定协议码 SHELL_NOT_FOUND。
var ErrShellNotFound = errors.New("SHELL_NOT_FOUND")
