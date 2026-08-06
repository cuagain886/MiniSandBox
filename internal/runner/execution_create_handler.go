package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"minisandbox/pkg/protocol"
)

// ExecutionLaunchRequest 是 HTTP 严格解码和基础 validation 后交给 execution service 的解耦副本。
type ExecutionLaunchRequest struct {
	// Validated 包含 argv/shell、已解析 timeout 和前后台标志。
	Validated ValidatedRequest
	// CWD 仍需由 execution service 在 workspace 安全边界内解析。
	CWD string
	// Env 仍需由 execution service 与已清洗的 image env 合并。
	Env map[string]string
}

// ExecutionHandle 是启动成功后交给 transport 的只读句柄，不暴露 PID、PGID 或取消 goroutine。
type ExecutionHandle struct {
	// ExecutionID 是 Manager 已注册、可立即查询的稳定 ID。
	ExecutionID ExecutionID
	// Events 是该 execution 的有序内存事件源。
	Events *EventStore
}

// ForegroundLauncher 在 headers 提交前完成校验、Manager 注册和用户进程启动接受。
// 实现负责 OS/process 细节，HTTP handler 不得直接调用这些能力。
type ForegroundLauncher interface {
	// StartForeground 启动前台 execution；ctx 断开由前台生命周期映射为取消。
	StartForeground(ctx context.Context, request ExecutionLaunchRequest) (*ExecutionHandle, error)
}

// ForegroundStreamFunc 接管已启动 execution 的 HTTP 流；调用前 handler 尚未提交响应 headers。
type ForegroundStreamFunc func(http.ResponseWriter, *http.Request, *ExecutionHandle)

// ExecutionCreateHandlerConfig 配置严格创建入口；后台分支由 P2-045 增量加入。
type ExecutionCreateHandlerConfig struct {
	// MaxRequestBytes 是 JSON body 的不可扩展硬上限。
	MaxRequestBytes int64
	// Validator 执行稳定 execution 请求语义校验。
	Validator *RequestValidator
	// ForegroundLauncher 负责前台 execution 启动。
	ForegroundLauncher ForegroundLauncher
	// ForegroundStream 在启动成功后接管响应。
	ForegroundStream ForegroundStreamFunc
}

// NewExecutionCreateHandler 创建 POST `/v1/executions` handler。
func NewExecutionCreateHandler(config ExecutionCreateHandlerConfig) (http.Handler, error) {
	if config.MaxRequestBytes <= 0 || config.Validator == nil || config.ForegroundLauncher == nil || config.ForegroundStream == nil {
		return nil, errors.New("execution create handler is not configured")
	}
	return &executionCreateHandler{config: config}, nil
}

type executionCreateHandler struct {
	config ExecutionCreateHandlerConfig
}

func (h *executionCreateHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRunnerError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed", false)
		return
	}
	if !mediaTypeMatches(request.Header.Get("Content-Type"), "application/json") {
		writeRunnerError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "content type must be application/json", false)
		return
	}
	decoded, err := decodeExecuteRequest(w, request, h.config.MaxRequestBytes)
	if err != nil {
		writeRunnerError(w, http.StatusBadRequest, string(protocol.ErrorCodeInvalidExecutionRequest), "execution request is invalid", false)
		return
	}
	validated, err := h.config.Validator.Validate(decoded)
	if err != nil {
		writeRunnerError(w, http.StatusUnprocessableEntity, string(protocol.ErrorCodeInvalidExecutionRequest), "execution request is invalid", false)
		return
	}
	if validated.Background {
		writeRunnerError(w, http.StatusBadRequest, string(protocol.ErrorCodeInvalidExecutionRequest), "background execution is not available", false)
		return
	}
	if !acceptsMediaType(request.Header.Values("Accept"), "text/event-stream") {
		writeRunnerError(w, http.StatusNotAcceptable, "NOT_ACCEPTABLE", "accept must include text/event-stream", false)
		return
	}
	launchRequest := ExecutionLaunchRequest{
		Validated: validated,
		CWD:       decoded.Cwd,
		Env:       cloneStringMap(decoded.Env),
	}
	handle, err := h.config.ForegroundLauncher.StartForeground(request.Context(), launchRequest)
	if err != nil {
		writeLaunchError(w, err)
		return
	}
	if handle == nil || handle.ExecutionID == "" || handle.Events == nil {
		writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution could not be started", false)
		return
	}
	h.config.ForegroundStream(w, request, handle)
}

func decodeExecuteRequest(w http.ResponseWriter, request *http.Request, limit int64) (protocol.ExecuteRequest, error) {
	body := http.MaxBytesReader(w, request.Body, limit)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var decoded protocol.ExecuteRequest
	if err := decoder.Decode(&decoded); err != nil {
		return protocol.ExecuteRequest{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return protocol.ExecuteRequest{}, errors.New("execution request has trailing JSON")
	}
	return decoded, nil
}

func mediaTypeMatches(value, want string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, want)
}

func acceptsMediaType(values []string, want string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil || parameters["q"] == "0" {
				continue
			}
			if strings.EqualFold(mediaType, want) || mediaType == "*/*" {
				return true
			}
		}
	}
	return false
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func writeLaunchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrExecutionLimitReached):
		writeRunnerError(w, http.StatusTooManyRequests, string(protocol.ErrorCodeExecutionLimitReached), "execution concurrency limit reached", true)
	case errors.Is(err, ErrRunnerShuttingDown):
		writeRunnerError(w, http.StatusServiceUnavailable, "RUNNER_SHUTTING_DOWN", "runner is shutting down", true)
	case errors.Is(err, ErrInvalidCWD):
		writeRunnerError(w, http.StatusUnprocessableEntity, string(protocol.ErrorCodeInvalidCWD), "execution working directory is invalid", false)
	case errors.Is(err, ErrShellNotFound):
		writeRunnerError(w, http.StatusUnprocessableEntity, string(protocol.ErrorCodeShellNotFound), "execution shell is unavailable", false)
	case errors.Is(err, ErrProcessStartFailed):
		writeRunnerError(w, http.StatusUnprocessableEntity, "EXECUTION_START_FAILED", "execution could not be started", false)
	default:
		writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution could not be started", false)
	}
}

func writeRunnerError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: protocol.ErrorDetail{
		Code:      code,
		Message:   message,
		RequestID: "",
		Retryable: retryable,
	}})
}
