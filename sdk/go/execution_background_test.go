package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestBackgroundExecutionFacade 验证 SDK 封装公开路径、cursor 和 HTTP 方法。
func TestBackgroundExecutionFacade(t *testing.T) {
	requests := make(chan *http.Request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			var request protocol.ExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode execute request: %v", err)
			}
			if !request.Background || request.TimeoutSeconds != 5 {
				t.Errorf("unexpected execute request: %#v", request)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(protocol.ExecutionDescriptor{
				ExecutionID: "e1",
				State:       protocol.ExecutionStatePending,
			})
		case r.Method == http.MethodGet && r.URL.Query().Has("cursor"):
			_ = json.NewEncoder(w).Encode(protocol.ExecutionLogPage{
				Events:     []protocol.ExecutionEvent{},
				NextCursor: 7,
				Complete:   false,
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(protocol.ExecutionStatus{
				ExecutionID: "e1",
				State:       protocol.ExecutionStateRunning,
			})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	ctx := context.Background()
	if _, err := client.StartBackgroundExecution(ctx, "sbx/one", ExecuteRequest{
		Shell: "go test ./...", Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatalf("start background execution: %v", err)
	}
	if _, err := client.GetExecution(ctx, "sbx/one", "exec/two"); err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if _, err := client.GetExecutionLogs(ctx, "sbx/one", "exec/two", 7); err != nil {
		t.Fatalf("get logs: %v", err)
	}
	if err := client.CancelExecution(ctx, "sbx/one", "exec/two"); err != nil {
		t.Fatalf("cancel execution: %v", err)
	}

	wantPaths := []string{
		"/v1/sandboxes/sbx%2Fone/executions",
		"/v1/sandboxes/sbx%2Fone/executions/exec%2Ftwo",
		"/v1/sandboxes/sbx%2Fone/executions/exec%2Ftwo/logs",
		"/v1/sandboxes/sbx%2Fone/executions/exec%2Ftwo",
	}
	for index, want := range wantPaths {
		request := <-requests
		if request.URL.EscapedPath() != want {
			t.Errorf("request %d path: got %s, want %s", index, request.URL.EscapedPath(), want)
		}
		if index == 2 && request.URL.Query().Get("cursor") != "7" {
			t.Errorf("logs cursor: got %q, want 7", request.URL.Query().Get("cursor"))
		}
	}
}
