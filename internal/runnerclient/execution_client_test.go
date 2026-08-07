package runnerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"minisandbox/pkg/protocol"
)

func TestClientFixedExecutionOperations(t *testing.T) {
	events := validStreamEvents()
	client := New("fixed.sock", "secret-token")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "http" || request.URL.Host != "runner" || request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("request authority escaped fixed client: %s", request.URL.String())
		}
		switch request.Method + " " + request.URL.RequestURI() {
		case "POST /v1/executions":
			var execute protocol.ExecuteRequest
			if err := json.NewDecoder(request.Body).Decode(&execute); err != nil {
				t.Fatalf("decode execute request: %v", err)
			}
			if execute.Background {
				return jsonResponse(http.StatusAccepted, protocol.ExecutionDescriptor{ExecutionID: "exec_test", State: protocol.ExecutionStateRunning}), nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(encodeClientFrames(t, events, "\n")))}, nil
		case "GET /v1/executions/exec_test":
			return jsonResponse(http.StatusOK, protocol.ExecutionStatus{ExecutionID: "exec_test", State: protocol.ExecutionStateRunning}), nil
		case "DELETE /v1/executions/exec_test":
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		case "GET /v1/executions/exec_test/logs?cursor=0":
			return jsonResponse(http.StatusOK, protocol.ExecutionLogPage{Events: events, NextCursor: events[len(events)-1].Sequence, Complete: true}), nil
		default:
			t.Fatalf("unexpected operation: %s %s", request.Method, request.URL.RequestURI())
			return nil, nil
		}
	})}

	stream, err := client.ExecuteForeground(context.Background(), protocol.ExecuteRequest{Argv: []string{"true"}})
	if err != nil {
		t.Fatalf("foreground: %v", err)
	}
	count := 0
	if err := stream.Consume(func(protocol.ExecutionEvent) error { count++; return nil }); err != nil || count != len(events) {
		t.Fatalf("consume foreground: count=%d err=%v", count, err)
	}
	descriptor, err := client.ExecuteBackground(context.Background(), protocol.ExecuteRequest{Argv: []string{"true"}})
	if err != nil || descriptor.ExecutionID != "exec_test" {
		t.Fatalf("background: %+v err=%v", descriptor, err)
	}
	status, err := client.Status(context.Background(), "exec_test")
	if err != nil || status.State != protocol.ExecutionStateRunning {
		t.Fatalf("status: %+v err=%v", status, err)
	}
	if err := client.Cancel(context.Background(), "exec_test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	page, err := client.Logs(context.Background(), "exec_test", 0)
	if err != nil || !page.Complete || len(page.Events) != len(events) {
		t.Fatalf("logs: %+v err=%v", page, err)
	}
}

func TestClientEscapesExecutionIDAndClosesBoundedResponses(t *testing.T) {
	closed := false
	client := New("fixed.sock", "token")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.EscapedPath() != "/v1/executions/..%2F..%2Fhealthz" {
			t.Fatalf("execution ID was not one escaped segment: %s", request.URL.EscapedPath())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: &callbackReadCloser{Reader: strings.NewReader(strings.Repeat("x", int(maxRunnerResponseBytes)+1)), close: func() { closed = true }}, Header: make(http.Header)}, nil
	})}
	if _, err := client.Status(context.Background(), "../../healthz"); err == nil {
		t.Fatal("oversized response accepted")
	}
	if !closed {
		t.Fatal("oversized response body was not closed")
	}
}

func TestClientOperationsHonorCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := New("fixed.sock", "token")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	_, err := client.Status(ctx, "exec_test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}

func jsonResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: http.Header{"Content-Type": []string{"application/json"}}}
}

type callbackReadCloser struct {
	io.Reader
	close func()
}

func (r *callbackReadCloser) Close() error {
	if r.close != nil {
		r.close()
	}
	return nil
}

func TestEventStreamCannotBeConsumedTwice(t *testing.T) {
	events := validStreamEvents()
	stream := &EventStream{body: io.NopCloser(strings.NewReader(encodeClientFrames(t, events, "\n")))}
	if err := stream.Consume(func(protocol.ExecutionEvent) error { return nil }); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := stream.Consume(func(protocol.ExecutionEvent) error { return nil }); err == nil {
		t.Fatal("stream consumed twice")
	}
}
