package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

const maxExecutionRequestBodyBytes int64 = 1 << 20

// ExecutionService 定义公共 execution handler 可调用的应用层用例。
type ExecutionService interface {
	// Execute 在 application 完成 sandbox admission 后返回前台 stream 或后台 descriptor。
	Execute(context.Context, application.Execute) (application.ExecutionResult, error)
}

func registerExecutionRoutes(mux *http.ServeMux, service ExecutionService) {
	if service == nil {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/executions", notImplemented("command execution"))
	} else {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/executions", executeSandboxHandler(service))
	}
	mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}/executions/{execution_id}", notImplemented("execution cancellation"))
}

func executeSandboxHandler(service ExecutionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		request, err := decodeExecutionRequest(w, r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if request.Background {
			writeError(w, r, domain.ErrNotImplemented)
			return
		}
		if !acceptsExecutionMediaType(r.Header.Values("Accept"), "text/event-stream") {
			writeError(w, r, domain.ErrInvalidExecutionRequest)
			return
		}
		result, err := service.Execute(r.Context(), mapExecutionCommand(sandboxID, request))
		if err != nil {
			writeError(w, r, err)
			return
		}
		if result.Stream == nil || result.Descriptor != nil {
			writeError(w, r, domain.ErrRunnerUnhealthy)
			return
		}
		defer result.Stream.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		_ = result.Stream.Consume(func(event protocol.ExecutionEvent) error {
			if event.Validate() != nil {
				return errors.New("runner returned an invalid execution event")
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			frame := "id: " + strconv.FormatUint(event.Sequence, 10) + "\n" +
				"event: " + string(event.Type) + "\n" +
				"data: " + string(encoded) + "\n\n"
			if _, err := io.WriteString(w, frame); err != nil {
				return err
			}
			return controller.Flush()
		})
	}
}

func decodeExecutionRequest(w http.ResponseWriter, r *http.Request) (protocol.ExecuteRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	body := http.MaxBytesReader(w, r.Body, maxExecutionRequestBodyBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request protocol.ExecuteRequest
	if err := decoder.Decode(&request); err != nil {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > int64((time.Duration(1<<63-1))/time.Second) {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if !mapExecutionSpec(request).Valid() {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	return request, nil
}

func mapExecutionCommand(sandboxID string, request protocol.ExecuteRequest) application.Execute {
	return application.Execute{SandboxID: sandboxID, Spec: mapExecutionSpec(request), Background: request.Background}
}

func mapExecutionSpec(request protocol.ExecuteRequest) domain.ExecutionSpec {
	environment := make(map[string]string, len(request.Env))
	for key, value := range request.Env {
		environment[key] = value
	}
	if request.Env == nil {
		environment = nil
	}
	return domain.ExecutionSpec{Argv: append([]string(nil), request.Argv...), Shell: request.Shell, Env: environment, Cwd: request.Cwd, Timeout: time.Duration(request.TimeoutSeconds) * time.Second}
}

func acceptsExecutionMediaType(values []string, want string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err == nil && parameters["q"] != "0" && (strings.EqualFold(mediaType, want) || mediaType == "*/*") {
				return true
			}
		}
	}
	return false
}
