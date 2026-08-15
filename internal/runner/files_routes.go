package runner

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerfiles"
	"minisandbox/pkg/protocol"
)

// filesJSONRequestLimit 是文件 JSON 请求体的字节上限；内容上传走独立
// 流式通道，不受该上限约束。
const filesJSONRequestLimit = 64 * 1024

// FilesRoutes 是 runner 固定的 workspace 文件路由组。
//
// 每个字段对应一个内部固定 endpoint；handler 只做严格解码、调用
// runnerfiles.Service 和错误映射，不解释路径语义。
type FilesRoutes struct {
	// Stat 处理 POST /v1/files/stat。
	Stat http.Handler
	// List 处理 POST /v1/directories/list。
	List http.Handler
	// Mkdir 处理 POST /v1/directories。
	Mkdir http.Handler
	// Upload 处理 PUT /v1/files/content。
	Upload http.Handler
	// Download 处理 GET /v1/files/content。
	Download http.Handler
	// Move 处理 POST /v1/files/move。
	Move http.Handler
	// Delete 处理 POST /v1/files/delete。
	Delete http.Handler
}

// NewFilesRoutes 把 workspace 文件服务包装为固定 HTTP handlers。
func NewFilesRoutes(
	service *runnerfiles.Service,
	features runnerbootstrap.Features,
) (*FilesRoutes, error) {
	if service == nil {
		return nil, errors.New("files service is not configured")
	}
	return &FilesRoutes{
		Stat:     filesStatHandler(service),
		List:     filesListHandler(service),
		Mkdir:    filesMkdirHandler(service),
		Upload:   filesUploadHandler(service, features.MaxUploadBytes),
		Download: filesDownloadHandler(service, features.MaxDownloadBytes),
		Move:     filesMoveHandler(service),
		Delete:   filesDeleteHandler(service),
	}, nil
}

func filesStatHandler(service *runnerfiles.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.FileStatRequest
		if !decodeFilesJSON(w, r, &request) {
			return
		}
		stat, err := service.Stat(request.Path)
		if writeFilesError(w, err) {
			return
		}
		writeFilesJSON(w, http.StatusOK, stat)
	})
}

func filesListHandler(service *runnerfiles.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.DirectoryListRequest
		if !decodeFilesJSON(w, r, &request) {
			return
		}
		listing, err := service.List(request.Path)
		if writeFilesError(w, err) {
			return
		}
		writeFilesJSON(w, http.StatusOK, listing)
	})
}

func filesMkdirHandler(service *runnerfiles.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.MkdirRequest
		if !decodeFilesJSON(w, r, &request) {
			return
		}
		created, stat, err := service.Mkdir(request.Path, request.Parents)
		if writeFilesError(w, err) {
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeFilesJSON(w, status, stat)
	})
}

func filesUploadHandler(service *runnerfiles.Service, maxUploadBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		overwrite := parseFilesBool(w, r, "overwrite", false)
		if overwrite == nil {
			return
		}
		createParents := parseFilesBool(w, r, "create_parents", false)
		if createParents == nil {
			return
		}
		replaced, stat, err := service.Upload(path, r.Body, *overwrite, *createParents, maxUploadBytes)
		if writeFilesError(w, err) {
			return
		}
		status := http.StatusCreated
		if replaced {
			status = http.StatusOK
		}
		writeFilesJSON(w, status, stat)
	})
}

func filesDownloadHandler(service *runnerfiles.Service, maxDownloadBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		download, err := service.Download(path, maxDownloadBytes)
		if writeFilesError(w, err) {
			return
		}
		defer download.Reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(download.Stat.SizeBytes, 10))
		w.WriteHeader(http.StatusOK)
		// 客户端断开或传输错误立即终止；文件内容不经过内存缓冲。
		if _, err := io.Copy(w, download.Reader); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
	})
}

func filesMoveHandler(service *runnerfiles.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.FileMoveRequest
		if !decodeFilesJSON(w, r, &request) {
			return
		}
		stat, err := service.Move(request.Source, request.Destination, request.Overwrite)
		if writeFilesError(w, err) {
			return
		}
		writeFilesJSON(w, http.StatusOK, stat)
	})
}

func filesDeleteHandler(service *runnerfiles.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.FileDeleteRequest
		if !decodeFilesJSON(w, r, &request) {
			return
		}
		if err := service.Delete(request.Path, request.Recursive); err != nil {
			writeFilesError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// decodeFilesJSON 严格解码文件 JSON 请求体。
func decodeFilesJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body := http.MaxBytesReader(w, r.Body, filesJSONRequestLimit)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRunnerError(w, http.StatusBadRequest, "INVALID_FILE_PATH", "files request body is invalid", false)
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeRunnerError(w, http.StatusBadRequest, "INVALID_FILE_PATH", "files request body is invalid", false)
		return false
	}
	return true
}

// parseFilesBool 解析可选布尔 query 参数；非法值返回 nil 并已写响应。
func parseFilesBool(w http.ResponseWriter, r *http.Request, name string, fallback bool) *bool {
	value := r.URL.Query().Get(name)
	if value == "" {
		return &fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		writeRunnerError(w, http.StatusBadRequest, "INVALID_FILE_PATH", "files query parameter is invalid", false)
		return nil
	}
	return &parsed
}

// writeFilesJSON 写出文件 JSON 响应。
func writeFilesJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeFilesError 把 runnerfiles 稳定错误映射为公共 runner 错误响应。
//
// 映射只使用稳定错误标识；errno 原文和绝对路径不会进入响应。
func writeFilesError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, runnerfiles.ErrInvalidPath):
		writeRunnerError(w, http.StatusBadRequest, "INVALID_FILE_PATH", "workspace path is invalid", false)
	case errors.Is(err, runnerfiles.ErrNotFound):
		writeRunnerError(w, http.StatusNotFound, "FILE_NOT_FOUND", "workspace path does not exist", false)
	case errors.Is(err, runnerfiles.ErrTypeMismatch):
		writeRunnerError(w, http.StatusConflict, "FILE_TYPE_MISMATCH", "workspace path type does not match the operation", false)
	case errors.Is(err, runnerfiles.ErrConflict):
		writeRunnerError(w, http.StatusConflict, "FILE_CONFLICT", "workspace path conflicts with an existing entry", false)
	case errors.Is(err, runnerfiles.ErrTooLarge):
		writeRunnerError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "file exceeds the configured size limit", false)
	case errors.Is(err, runnerfiles.ErrUnavailable):
		writeRunnerError(w, http.StatusServiceUnavailable, "FILES_UNAVAILABLE", "files capability is unavailable", true)
	default:
		// 传输中断类错误按可重试处理，其余保守视为内部错误。
		retryable := errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
		writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "files operation failed", retryable)
	}
	return true
}

// NewCapabilitiesHandler 返回固定 capabilities 响应 handler。
//
// 响应来自 bootstrap 快照；请求不能修改任何能力开关或上限。
func NewCapabilitiesHandler(capabilities protocol.Capabilities) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFilesJSON(w, http.StatusOK, capabilities)
	})
}
