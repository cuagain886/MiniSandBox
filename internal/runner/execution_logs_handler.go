package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"minisandbox/pkg/protocol"
)

const executionLogsPathSuffix = "/logs"

// NewExecutionLogsHandler 创建非长轮询的 cursor 日志读取 handler。
func NewExecutionLogsHandler(manager *Manager, reader *BackgroundLogReader) (http.Handler, error) {
	if manager == nil || reader == nil {
		return nil, errors.New("execution logs handler is not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRunnerError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed", false)
			return
		}
		id, ok := executionLogIDFromPath(request.URL)
		if !ok {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution log was not found", false)
			return
		}
		if _, err := manager.Descriptor(id); errors.Is(err, ErrExecutionNotFound) {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution log was not found", false)
			return
		}
		cursor, err := parseLogCursor(request.URL.Query())
		if err != nil {
			writeRunnerError(w, http.StatusBadRequest, "INVALID_LOG_CURSOR", "log cursor is invalid", false)
			return
		}
		page, err := reader.Read(id, cursor)
		switch {
		case errors.Is(err, ErrBackgroundLogNotFound):
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution log was not found", false)
			return
		case errors.Is(err, ErrInvalidLogCursor):
			writeRunnerError(w, http.StatusBadRequest, "INVALID_LOG_CURSOR", "log cursor is invalid", false)
			return
		case err != nil:
			writeRunnerError(w, http.StatusInternalServerError, "EXECUTION_LOG_CORRUPT", "execution log is unavailable", false)
			return
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution log is unavailable", false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}), nil
}

func executionLogIDFromPath(value *url.URL) (ExecutionID, bool) {
	if value == nil || !strings.HasPrefix(value.EscapedPath(), executionStatusPathPrefix) || !strings.HasSuffix(value.EscapedPath(), executionLogsPathSuffix) {
		return "", false
	}
	escaped := strings.TrimSuffix(strings.TrimPrefix(value.EscapedPath(), executionStatusPathPrefix), executionLogsPathSuffix)
	if escaped == "" || strings.ContainsAny(escaped, "/\\") {
		return "", false
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || !validStoredExecutionID(ExecutionID(decoded)) {
		return "", false
	}
	return ExecutionID(decoded), true
}

func parseLogCursor(values url.Values) (uint64, error) {
	cursors, exists := values["cursor"]
	if !exists {
		return 0, nil
	}
	if len(cursors) != 1 || cursors[0] == "" || strings.HasPrefix(cursors[0], "+") || strings.HasPrefix(cursors[0], "-") {
		return 0, ErrInvalidLogCursor
	}
	cursor, err := strconv.ParseUint(cursors[0], 10, 64)
	if err != nil || strconv.FormatUint(cursor, 10) != cursors[0] {
		return 0, ErrInvalidLogCursor
	}
	return cursor, nil
}
