package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"minisandbox/pkg/protocol"
)

// FileStat 是 workspace 文件与目录条目的 metadata 模型。
type FileStat = protocol.FileStat

// FileType 是文件条目类型枚举。
type FileType = protocol.FileType

// 文件类型常量，与公共协议保持一致。
const (
	FileTypeRegular   = protocol.FileTypeRegular
	FileTypeDirectory = protocol.FileTypeDirectory
	FileTypeSymlink   = protocol.FileTypeSymlink
	FileTypeOther     = protocol.FileTypeOther
)

// UploadOption 是 Files.Upload 支持的可选语义。
type UploadOption func(*uploadOptions)

// uploadOptions 汇总 UploadOption 展开后的可选语义。
type uploadOptions struct {
	overwrite     bool
	createParents bool
}

// WithOverwrite 允许上传原子替换已存在的普通文件。
func WithOverwrite() UploadOption {
	return func(options *uploadOptions) { options.overwrite = true }
}

// WithCreateParents 自动创建缺失的父目录。
func WithCreateParents() UploadOption {
	return func(options *uploadOptions) { options.createParents = true }
}

// Files 返回当前 sandbox 的 workspace 文件管理对象。
//
// 返回的对象无状态，可并发复用；所有路径都使用 workspace 相对规则，
// "." 表示根目录。
func (s *Sandbox) Files() *SandboxFiles {
	return &SandboxFiles{sandbox: s}
}

// SandboxFiles 提供 workspace 文件的 SDK 易用接口。
type SandboxFiles struct {
	sandbox *Sandbox
}

func (f *SandboxFiles) basePath() string {
	return "/v1/sandboxes/" + url.PathEscape(f.sandbox.id)
}

// Stat 查询一个路径的 metadata。
func (f *SandboxFiles) Stat(ctx context.Context, path string) (FileStat, error) {
	var stat FileStat
	err := f.sandbox.client.doJSON(
		ctx,
		http.MethodPost,
		f.basePath()+"/files/stat",
		protocol.FileStatRequest{Path: path},
		&stat,
	)
	return stat, err
}

// List 返回目录直接子项，按名称排序。
func (f *SandboxFiles) List(ctx context.Context, path string) ([]FileStat, error) {
	var listing protocol.DirectoryListing
	err := f.sandbox.client.doJSON(
		ctx,
		http.MethodPost,
		f.basePath()+"/directories/list",
		protocol.DirectoryListRequest{Path: path},
		&listing,
	)
	if err != nil {
		return nil, err
	}
	return listing.Entries, nil
}

// Mkdir 创建目录；parents 为 true 时创建缺失祖先并接受已存在目录。
func (f *SandboxFiles) Mkdir(ctx context.Context, path string, parents bool) (FileStat, error) {
	encoded, err := json.Marshal(protocol.MkdirRequest{Path: path, Parents: parents})
	if err != nil {
		return FileStat{}, err
	}
	var stat FileStat
	status, err := f.requestJSONStatus(ctx, http.MethodPost, f.basePath()+"/directories", encoded, &stat)
	if err != nil {
		return FileStat{}, err
	}
	_ = status
	return stat, nil
}

// Upload 把 reader 的内容流式上传到一个 workspace 文件。
//
// 上传是原子的：目标只在完整写入后可见。reader 不会被自动重放，
// 失败重试由调用方重新提供。
func (f *SandboxFiles) Upload(ctx context.Context, path string, reader io.Reader, options ...UploadOption) error {
	var resolved uploadOptions
	for _, option := range options {
		option(&resolved)
	}
	query := url.Values{}
	query.Set("path", path)
	query.Set("overwrite", strconv.FormatBool(resolved.overwrite))
	query.Set("create_parents", strconv.FormatBool(resolved.createParents))
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		f.sandbox.client.baseURL+f.basePath()+"/files/content?"+query.Encode(),
		reader,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	return f.sandbox.client.doStream(request, nil)
}

// Download 打开一个 workspace 普通文件的流式下载；调用方负责关闭。
func (f *SandboxFiles) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	query := url.Values{}
	query.Set("path", path)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		f.sandbox.client.baseURL+f.basePath()+"/files/content?"+query.Encode(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	return f.sandbox.client.doStreamBody(request)
}

// Move 在 workspace 内移动（重命名）路径。
func (f *SandboxFiles) Move(ctx context.Context, source, destination string, overwrite bool) (FileStat, error) {
	var stat FileStat
	err := f.sandbox.client.doJSON(
		ctx,
		http.MethodPost,
		f.basePath()+"/files/move",
		protocol.FileMoveRequest{Source: source, Destination: destination, Overwrite: overwrite},
		&stat,
	)
	return stat, err
}

// Delete 删除文件或目录；目标不存在时同样成功。删除非空目录必须显式
// 指定 recursive，且不会跟随符号链接。
func (f *SandboxFiles) Delete(ctx context.Context, path string, recursive bool) error {
	return f.sandbox.client.doJSON(
		ctx,
		http.MethodPost,
		f.basePath()+"/files/delete",
		protocol.FileDeleteRequest{Path: path, Recursive: recursive},
		nil,
	)
}

// requestJSONStatus 发送 JSON 请求并在 200/201 下解码，返回实际状态码。
func (f *SandboxFiles) requestJSONStatus(ctx context.Context, method, path string, encoded []byte, output any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, f.sandbox.client.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	var statusCode int
	err = f.sandbox.client.doStream(request, func(response *http.Response) error {
		statusCode = response.StatusCode
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
			return fmt.Errorf("minisandbox: unexpected HTTP status %d", response.StatusCode)
		}
		return json.NewDecoder(response.Body).Decode(output)
	})
	return statusCode, err
}
