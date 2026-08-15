package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// filesJSONRequestLimit 是公共文件 JSON 请求体的解码上限。
const filesJSONRequestLimit = 64 * 1024

// FilesService 定义公共文件 handler 允许调用的应用层用例。
//
// 接口只承载请求解析与转发；文件语义全部在 runner 内实现。
type FilesService interface {
	// Stat 查询单个 workspace 路径的 metadata。
	Stat(ctx context.Context, sandboxID string, request protocol.FileStatRequest) (protocol.FileStat, error)
	// List 列出目录直接子项。
	List(ctx context.Context, sandboxID string, request protocol.DirectoryListRequest) (protocol.DirectoryListing, error)
	// Mkdir 创建目录并区分新建与已存在。
	Mkdir(ctx context.Context, sandboxID string, request protocol.MkdirRequest) (bool, protocol.FileStat, error)
	// Upload 流式上传到 workspace 文件。
	Upload(ctx context.Context, sandboxID, path string, content io.Reader, overwrite, createParents bool) (bool, protocol.FileStat, error)
	// Download 打开 workspace 普通文件的流式下载。
	Download(ctx context.Context, sandboxID, path string) (io.ReadCloser, protocol.FileStat, error)
	// Move 在 workspace 内移动路径。
	Move(ctx context.Context, sandboxID string, request protocol.FileMoveRequest) (protocol.FileStat, error)
	// Delete 幂等删除 workspace 文件或目录。
	Delete(ctx context.Context, sandboxID string, request protocol.FileDeleteRequest) error
	// Capabilities 返回当前 sandbox runner 的能力集合。
	Capabilities(ctx context.Context, sandboxID string) (protocol.Capabilities, error)
}

// NewSandboxCapabilitiesHandler 返回公共 capabilities 查询 handler。
func NewSandboxCapabilitiesHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validSandboxID(r.PathValue("sandbox_id")) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		capabilities, err := service.Capabilities(r.Context(), r.PathValue("sandbox_id"))
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, capabilities)
	})
}

// NewFileStatHandler 返回公共文件 metadata 查询 handler。
func NewFileStatHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		var request protocol.FileStatRequest
		if !decodeFilesJSONBody(w, r, &request) {
			return
		}
		stat, err := service.Stat(r.Context(), sandboxID, request)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, stat)
	})
}

// NewDirectoryListHandler 返回公共目录列表 handler。
func NewDirectoryListHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		var request protocol.DirectoryListRequest
		if !decodeFilesJSONBody(w, r, &request) {
			return
		}
		listing, err := service.List(r.Context(), sandboxID, request)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, listing)
	})
}

// NewDirectoryCreateHandler 返回公共目录创建 handler。
func NewDirectoryCreateHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		var request protocol.MkdirRequest
		if !decodeFilesJSONBody(w, r, &request) {
			return
		}
		created, stat, err := service.Mkdir(r.Context(), sandboxID, request)
		if err != nil {
			writeError(w, r, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, stat)
	})
}

// NewFileUploadHandler 返回公共流式上传 handler。
func NewFileUploadHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		path := r.URL.Query().Get("path")
		overwrite, ok := parseFilesQueryBool(w, r, "overwrite")
		if !ok {
			return
		}
		createParents, ok := parseFilesQueryBool(w, r, "create_parents")
		if !ok {
			return
		}
		if r.ContentLength > 0 && !hasOctetStreamContentType(r) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		replaced, stat, err := service.Upload(r.Context(), sandboxID, path, r.Body, overwrite, createParents)
		if err != nil {
			writeError(w, r, err)
			return
		}
		status := http.StatusCreated
		if replaced {
			status = http.StatusOK
		}
		writeJSON(w, status, stat)
	})
}

// NewFileDownloadHandler 返回公共流式下载 handler。
func NewFileDownloadHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		path := r.URL.Query().Get("path")
		reader, stat, err := service.Download(r.Context(), sandboxID, path)
		if err != nil {
			writeError(w, r, err)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(stat.SizeBytes, 10))
		w.WriteHeader(http.StatusOK)
		// 控制面不缓存文件内容；客户端断开时透传终止两侧流。
		if _, err := io.Copy(w, reader); err != nil {
			return
		}
	})
}

// NewFileMoveHandler 返回公共文件移动 handler。
func NewFileMoveHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		var request protocol.FileMoveRequest
		if !decodeFilesJSONBody(w, r, &request) {
			return
		}
		stat, err := service.Move(r.Context(), sandboxID, request)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, stat)
	})
}

// NewFileDeleteHandler 返回公共文件删除 handler。
func NewFileDeleteHandler(service FilesService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		var request protocol.FileDeleteRequest
		if !decodeFilesJSONBody(w, r, &request) {
			return
		}
		if err := service.Delete(r.Context(), sandboxID, request); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// decodeFilesJSONBody 严格解码公共文件 JSON 请求体。
func decodeFilesJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	body := http.MaxBytesReader(w, r.Body, filesJSONRequestLimit)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, r, domain.ErrInvalidFilePath)
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, r, domain.ErrInvalidFilePath)
		return false
	}
	return true
}

// parseFilesQueryBool 解析可选布尔 query 参数；非法值写出错误并返回 false。
func parseFilesQueryBool(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return false, true
	}
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	writeError(w, r, domain.ErrInvalidFilePath)
	return false, false
}

// hasOctetStreamContentType 校验上传内容类型声明。
func hasOctetStreamContentType(r *http.Request) bool {
	return r.Header.Get("Content-Type") == "application/octet-stream"
}
