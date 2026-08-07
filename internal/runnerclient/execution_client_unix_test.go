//go:build unix

package runnerclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"

	"minisandbox/pkg/protocol"
)

func TestExecutionClientUsesInjectedUnixSocket(t *testing.T) {
	socketPath := t.TempDir() + "/runner.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen Unix socket: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/executions/exec_test" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		response := protocol.ExecutionStatus{ExecutionID: "exec_test", State: protocol.ExecutionStateRunning}
		encoded := jsonResponse(http.StatusOK, response)
		defer encoded.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(encoded.StatusCode)
		_, _ = io.Copy(w, encoded.Body)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	client := New(socketPath, "token")
	status, err := client.Status(context.Background(), "exec_test")
	if err != nil || status.ExecutionID != "exec_test" {
		t.Fatalf("status through Unix socket: %+v err=%v", status, err)
	}
}
