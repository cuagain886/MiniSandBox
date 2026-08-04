package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
	sdk "minisandbox/sdk/go"
)

type cancelContract struct {
	StatusCode int `json:"status_code"`
	BodyBytes  int `json:"body_bytes"`
}

// TestPhase2FixtureMatrix 用同一组正反 fixtures 锁定 request、event、后台资源、
// cancel、error 和 health 的字段、枚举、单位及终态不变量。
func TestPhase2FixtureMatrix(t *testing.T) {
	tests := []struct {
		name     string
		valid    string
		invalids []string
		check    func(*testing.T, string) error
	}{
		{"request", "request.valid.json", []string{"request.invalid.json"}, checkRequestFixture},
		{"event", "event.valid.json", []string{"event.invalid.json"}, checkEventFixture},
		{"descriptor", "descriptor.valid.json", []string{"descriptor.invalid.json"}, checkDescriptorFixture},
		{"status", "status.valid.json", []string{"status.invalid.json"}, checkStatusFixture},
		{"cancel", "cancel.valid.json", []string{"cancel.invalid.json"}, checkCancelFixture},
		{"logs", "logs.valid.json", []string{"logs.invalid.json"}, checkLogsFixture},
		{"error", "error.valid.json", []string{"error.invalid.json"}, checkErrorFixture},
		{"health", "health.valid.json", []string{"health.invalid-version.json", "health.invalid-netns.json"}, checkHealthFixture},
	}
	for _, test := range tests {
		t.Run(test.name+"/positive", func(t *testing.T) {
			if err := test.check(t, test.valid); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
		for _, invalid := range test.invalids {
			t.Run(test.name+"/negative/"+invalid, func(t *testing.T) {
				if err := test.check(t, invalid); err == nil {
					t.Fatal("invalid fixture accepted")
				}
			})
		}
	}
}

// TestPhase2OpenAPIAndSDKUseMatrix 验证公共 API、runner API 与 SDK 消费同一
// request/descriptor/status/cancel/logs/error 契约；仅使用内存 transport。
func TestPhase2OpenAPIAndSDKUseMatrix(t *testing.T) {
	lifecycle, runner := readLifecycleOpenAPI(t), readRunnerOpenAPI(t)
	for _, fragment := range []string{"ExecuteRequest:", "ExecutionEvent:", "ExecutionDescriptor:", "ExecutionStatus:", "ExecutionLogPage:", "ErrorResponse:"} {
		if !strings.Contains(lifecycle, fragment) || !strings.Contains(runner, fragment) {
			t.Fatalf("shared schema missing from external or runner API: %s", fragment)
		}
	}
	for _, document := range []string{lifecycle, runner} {
		if !strings.Contains(document, "operationId: cancel") || !strings.Contains(document, `"202":`) || !strings.Contains(document, `"204":`) {
			t.Fatal("cancel 202/204 contract drift")
		}
	}

	requestFixture := decodePhase2Fixture[protocol.ExecuteRequest](t, "request.valid.json")
	descriptorBody := readPhase2Fixture(t, "descriptor.valid.json")
	statusBody := readPhase2Fixture(t, "status.valid.json")
	logsBody := readPhase2Fixture(t, "logs.valid.json")
	errorBody := readPhase2Fixture(t, "error.valid.json")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		path := request.URL.Path
		switch {
		case request.Method == http.MethodPost:
			var got protocol.ExecuteRequest
			if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
				t.Errorf("decode SDK request: %v", err)
			}
			if !reflect.DeepEqual(got, requestFixture) {
				t.Errorf("SDK request drift: got %#v, want %#v", got, requestFixture)
			}
			return fixtureResponse(http.StatusAccepted, descriptorBody), nil
		case request.Method == http.MethodGet && strings.HasSuffix(path, "/logs"):
			return fixtureResponse(http.StatusOK, logsBody), nil
		case request.Method == http.MethodGet && strings.Contains(path, "/executions/error"):
			return fixtureResponse(http.StatusConflict, errorBody), nil
		case request.Method == http.MethodGet:
			return fixtureResponse(http.StatusOK, statusBody), nil
		case request.Method == http.MethodDelete:
			return fixtureResponse(http.StatusAccepted, nil), nil
		default:
			return nil, errors.New("unexpected SDK request")
		}
	})
	client := sdk.NewClient("http://minisandbox", &http.Client{Transport: transport})
	native := sdk.ExecuteRequest{Argv: []string{"go", "test", "./..."}, Cwd: "/workspace", Env: map[string]string{"CI": "true"}, Timeout: 120 * time.Second}
	descriptor, err := client.StartBackgroundExecution(context.Background(), "sandbox-1", native)
	if err != nil || descriptor.ExecutionID != "e1" {
		t.Fatalf("SDK descriptor fixture: %+v, %v", descriptor, err)
	}
	status, err := client.GetExecution(context.Background(), "sandbox-1", "e1")
	if err != nil || status.TerminalEvent == nil || !status.TerminalEvent.Terminal() {
		t.Fatalf("SDK status fixture: %+v, %v", status, err)
	}
	logs, err := client.GetExecutionLogs(context.Background(), "sandbox-1", "e1", 0)
	if err != nil || !logs.Complete || logs.NextCursor != 5 {
		t.Fatalf("SDK logs fixture: %+v, %v", logs, err)
	}
	if err := client.CancelExecution(context.Background(), "sandbox-1", "e1"); err != nil {
		t.Fatalf("SDK cancel fixture: %v", err)
	}
	_, err = client.GetExecution(context.Background(), "sandbox-1", "error")
	var responseErr *sdk.ResponseError
	if !errors.As(err, &responseErr) || responseErr.Detail.Code != string(protocol.ErrorCodeRunnerProtocolMismatch) || responseErr.Detail.Retryable {
		t.Fatalf("SDK error fixture: %T %v", err, err)
	}
}

func checkRequestFixture(t *testing.T, name string) error {
	r := decodePhase2Fixture[protocol.ExecuteRequest](t, name)
	if (len(r.Argv) == 0) == (r.Shell == "") || r.TimeoutSeconds < 0 || (r.Cwd != "" && r.Cwd != "/workspace" && !strings.HasPrefix(r.Cwd, "/workspace/")) {
		return errors.New("invalid execution request")
	}
	return nil
}

func checkEventFixture(t *testing.T, name string) error {
	return decodePhase2Fixture[protocol.ExecutionEvent](t, name).Validate()
}

func checkDescriptorFixture(t *testing.T, name string) error {
	d := decodePhase2Fixture[protocol.ExecutionDescriptor](t, name)
	if d.ExecutionID == "" || d.State != protocol.ExecutionStatePending && d.State != protocol.ExecutionStateRunning {
		return errors.New("invalid execution descriptor")
	}
	return nil
}

func checkStatusFixture(t *testing.T, name string) error {
	s := decodePhase2Fixture[protocol.ExecutionStatus](t, name)
	terminal := s.State == protocol.ExecutionStateExited || s.State == protocol.ExecutionStateFailed || s.State == protocol.ExecutionStateCancelled || s.State == protocol.ExecutionStateTimedOut
	if s.ExecutionID == "" || terminal != (s.TerminalEvent != nil) || s.TerminalEvent != nil && (s.TerminalEvent.ExecutionID != s.ExecutionID || !s.TerminalEvent.Terminal() || s.TerminalEvent.Validate() != nil) {
		return errors.New("invalid execution status")
	}
	return nil
}

func checkCancelFixture(t *testing.T, name string) error {
	c := decodePhase2Fixture[cancelContract](t, name)
	if c.StatusCode != http.StatusAccepted && c.StatusCode != http.StatusNoContent || c.BodyBytes != 0 {
		return errors.New("invalid cancel response")
	}
	return nil
}

func checkLogsFixture(t *testing.T, name string) error {
	p := decodePhase2Fixture[protocol.ExecutionLogPage](t, name)
	var last uint64
	for _, event := range p.Events {
		if event.Validate() != nil || event.Sequence <= last {
			return errors.New("invalid log event sequence")
		}
		last = event.Sequence
	}
	if len(p.Events) > 0 && p.NextCursor != last || p.Complete != (len(p.Events) > 0 && p.Events[len(p.Events)-1].Terminal()) {
		return errors.New("invalid log page cursor or completion")
	}
	return nil
}

func checkErrorFixture(t *testing.T, name string) error {
	e := decodePhase2Fixture[protocol.ErrorResponse](t, name).Error
	if e.Code != string(protocol.ErrorCodeRunnerProtocolMismatch) || e.Message == "" || e.RequestID == "" || e.Retryable {
		return errors.New("invalid error envelope")
	}
	return nil
}

func checkHealthFixture(t *testing.T, name string) error {
	h := decodePhase2Fixture[protocol.RunnerHealth](t, name)
	if h.Status != "ok" || h.Service != "runnerd" || h.Version == "" || h.ProtocolVersion != runnerbootstrap.CurrentProtocolVersion {
		return errors.New("invalid runner health version or service")
	}
	return protocol.ValidateRunnerNetNSIdentity(h.NetNSIdentity)
}

func decodePhase2Fixture[T any](t *testing.T, name string) T {
	t.Helper()
	content := readPhase2Fixture(t, name)
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode phase2 fixture %s: %v", name, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("phase2 fixture %s has trailing JSON", name)
	}
	return value
}

func readPhase2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate phase2 fixture source")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "fixtures", "phase2", name))
	if err != nil {
		t.Fatalf("read phase2 fixture %s: %v", name, err)
	}
	return bytes.TrimSpace(content)
}

func fixtureResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
