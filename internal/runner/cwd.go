package runner

import "errors"

// ErrInvalidCWD 是 execution cwd 缺失、越界、非目录或包含 symlink 时的稳定内部错误。
var ErrInvalidCWD = errors.New("invalid execution working directory")
