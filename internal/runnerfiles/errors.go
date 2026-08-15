// Package runnerfiles 提供 runner 内 workspace 文件服务的安全实现。
//
// 本包以 workspace 根目录 fd 为唯一解析起点，所有目标访问都通过
// openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS) 等 fd-relative syscall
// 完成，保证任何路径、symlink 或并发 rename 竞争都不能把操作带出
// workspace。本包不解析 HTTP，也不接触 runner 认证材料。
package runnerfiles

import "errors"

// 文件操作的稳定错误集合。
//
// 这些错误由 handler 层映射为公共错误码；实现不得把 errno 原文或绝对
// 路径拼进错误文本。
var (
	// ErrUnavailable 表示文件能力未启用或平台不支持所需的 fd-relative syscall。
	ErrUnavailable = errors.New("files capability unavailable")
	// ErrInvalidPath 表示路径违反公共 workspace 相对路径规则。
	ErrInvalidPath = errors.New("invalid workspace path")
	// ErrNotFound 表示目标路径不存在；delete 对缺失路径幂等成功。
	ErrNotFound = errors.New("path not found")
	// ErrTypeMismatch 表示操作目标类型不符，例如对目录下载内容。
	ErrTypeMismatch = errors.New("path type mismatch")
	// ErrConflict 表示非覆盖写入、移动或目录替换遇到冲突。
	ErrConflict = errors.New("path conflict")
	// ErrTooLarge 表示上传或下载超过配置的字节上限。
	ErrTooLarge = errors.New("file too large")
)
