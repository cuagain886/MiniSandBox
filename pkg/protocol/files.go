package protocol

import (
	"errors"
	"time"
	"unicode/utf8"
)

// files 路径与元数据的公共约束。
const (
	// MaxFilePathBytes 是单个 workspace 相对路径的最大字节数。
	MaxFilePathBytes = 4096
	// MaxFileNameBytes 是单个路径分段的最大字节数。
	MaxFileNameBytes = 255
)

// ErrInvalidFilePath 表示路径违反公共 workspace 相对路径规则。
var ErrInvalidFilePath = errors.New("invalid workspace file path")

// FileType 是文件 stat 与目录列表使用的稳定类型枚举。
type FileType string

const (
	// FileTypeRegular 表示普通文件。
	FileTypeRegular FileType = "regular"
	// FileTypeDirectory 表示目录。
	FileTypeDirectory FileType = "directory"
	// FileTypeSymlink 表示符号链接本体。
	FileTypeSymlink FileType = "symlink"
	// FileTypeOther 表示 fifo、socket、device 等不进一步识别的类型。
	FileTypeOther FileType = "other"
)

// FileStat 是文件与目录条目的公共 metadata 模型。
//
// 只暴露调用方可以安全读取的字段：不包含 uid/gid、inode、device、symlink
// 绝对目标或 xattr。Mode 是四位八进制权限字符串，例如 "0644"。
type FileStat struct {
	// Path 是 workspace 相对路径；根目录表示为 "."。
	Path string `json:"path"`
	// Type 是条目类型。
	Type FileType `json:"type"`
	// SizeBytes 是普通文件的字节长度；目录为 0。
	SizeBytes int64 `json:"size_bytes"`
	// Mode 是四位八进制权限字符串。
	Mode string `json:"mode"`
	// ModifiedAt 是最后修改时间，wire 使用 RFC3339 UTC。
	ModifiedAt time.Time `json:"modified_at"`
}

// FileStatRequest 是 stat 请求模型。
type FileStatRequest struct {
	// Path 是 workspace 相对路径；"." 表示根目录。
	Path string `json:"path"`
}

// Validate 校验路径符合公共 workspace 相对规则。
func (r FileStatRequest) Validate() error { return ValidateFilePath(r.Path) }

// DirectoryListRequest 是目录列表请求模型。
type DirectoryListRequest struct {
	// Path 是 workspace 相对目录路径；"." 表示根目录。
	Path string `json:"path"`
}

// Validate 校验路径符合公共 workspace 相对规则。
func (r DirectoryListRequest) Validate() error { return ValidateFilePath(r.Path) }

// DirectoryListing 是目录直接子项的响应模型。
type DirectoryListing struct {
	// Path 是被列出的目录路径。
	Path string `json:"path"`
	// Entries 按名称排序；空目录返回空数组而不是 null。
	Entries []FileStat `json:"entries"`
}

// MkdirRequest 是目录创建请求模型。
type MkdirRequest struct {
	// Path 是要创建的目录路径；不能是根目录。
	Path string `json:"path"`
	// Parents 表示是否创建缺失的父目录并接受已存在目录。
	Parents bool `json:"parents"`
}

// Validate 校验路径规则；根目录不能通过 mkdir 创建。
func (r MkdirRequest) Validate() error {
	if r.Path == "." {
		return ErrInvalidFilePath
	}
	return ValidateFilePath(r.Path)
}

// FileMoveRequest 是 workspace 内移动（重命名）请求模型。
type FileMoveRequest struct {
	// Source 是源路径。
	Source string `json:"source"`
	// Destination 是目标路径；两者都必须在同一 workspace 内。
	Destination string `json:"destination"`
	// Overwrite 表示是否替换已存在的目标普通文件。
	Overwrite bool `json:"overwrite"`
}

// Validate 校验两侧路径；根目录不能参与移动。
func (r FileMoveRequest) Validate() error {
	if r.Source == "." || r.Destination == "." {
		return ErrInvalidFilePath
	}
	if err := ValidateFilePath(r.Source); err != nil {
		return err
	}
	return ValidateFilePath(r.Destination)
}

// FileDeleteRequest 是删除请求模型。
type FileDeleteRequest struct {
	// Path 是要删除的路径；根目录不能删除。
	Path string `json:"path"`
	// Recursive 表示是否删除非空目录；不跟随符号链接。
	Recursive bool `json:"recursive"`
}

// Validate 校验路径规则；根目录不能删除。
func (r FileDeleteRequest) Validate() error {
	if r.Path == "." {
		return ErrInvalidFilePath
	}
	return ValidateFilePath(r.Path)
}

// ValidateFilePath 校验公共 workspace 相对路径规则。
//
// 合法路径必须是 UTF-8、非空、不超过 MaxFilePathBytes；"." 仅表示根目录；
// 其他路径以 "/" 分段，每段 1 到 MaxFileNameBytes 字节，不允许 "."、".."
// 段、反斜杠、NUL 和其他控制字符。服务端还会在内核层保证最终解析结果
// 不离开 workspace（openat2 RESOLVE_BENEATH），本函数只做协议层预检。
func ValidateFilePath(path string) error {
	if path == "" || path == "." {
		if path == "." {
			return nil
		}
		return ErrInvalidFilePath
	}
	if len(path) > MaxFilePathBytes || !utf8.ValidString(path) {
		return ErrInvalidFilePath
	}
	start := 0
	for index := 0; index <= len(path); index++ {
		if index < len(path) && path[index] != '/' {
			continue
		}
		segment := path[start:index]
		if segment == "" || segment == "." || segment == ".." ||
			len(segment) > MaxFileNameBytes || containsForbiddenPathByte(segment) {
			return ErrInvalidFilePath
		}
		start = index + 1
	}
	return nil
}

// containsForbiddenPathByte 报告分段是否包含反斜杠或控制字符。
func containsForbiddenPathByte(segment string) bool {
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if character == '\\' || character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
