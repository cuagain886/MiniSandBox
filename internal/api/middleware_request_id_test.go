package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"minisandbox/internal/observability/logging"
	"minisandbox/pkg/protocol"
)

// TestRequestIDMiddlewareAcceptsValidOrGeneratesForInvalid 验证合法透传，空值、超长和控制字符重新生成且不回显。
func TestRequestIDMiddlewareAcceptsValidOrGeneratesForInvalid(t *testing.T) {
	cases := []struct {
		name, input string
		want        string
	}{{"valid", "client.req-1", "client.req-1"}, {"empty", "", strings.Repeat("ab", 16)},
		{"too-long", strings.Repeat("x", 129), strings.Repeat("ab", 16)}, {"control", "secret\nheader", strings.Repeat("ab", 16)}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			seen := ""
			handler := requestIDMiddlewareWithRandom(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				id, ok := logging.RequestIDFromContext(request.Context())
				if !ok {
					t.Fatal("request ID missing from context")
				}
				seen = id.String()
			}), func(buffer []byte) (int, error) {
				copy(buffer, strings.Repeat("\xab", len(buffer)))
				return len(buffer), nil
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if testCase.input != "" {
				request.Header.Set(requestIDHeader, testCase.input)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if got := response.Header().Get(requestIDHeader); got != testCase.want || seen != testCase.want {
				t.Fatalf("request ID: header=%q context=%q want=%q", got, seen, testCase.want)
			}
		})
	}
}

// TestRequestIDMiddlewareUsesSameIDInErrorEnvelope 验证 handler、响应头和错误 envelope 使用同一 ID。
func TestRequestIDMiddlewareUsesSameIDInErrorEnvelope(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		writeError(w, request, errors.New("raw secret"))
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if header := response.Header().Get(requestIDHeader); header == "" || envelope.Error.RequestID != header {
		t.Fatalf("request ID mismatch: header=%q envelope=%q", header, envelope.Error.RequestID)
	}
}

// TestRequestIDMiddlewareRandomFailureFailsClosed 验证随机源失败不进入业务 handler 且不回显非法输入。
func TestRequestIDMiddlewareRandomFailureFailsClosed(t *testing.T) {
	called := false
	handler := requestIDMiddlewareWithRandom(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
		func([]byte) (int, error) { return 0, errors.New("entropy secret") })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(requestIDHeader, "bad control\nvalue")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if called || response.Code != http.StatusInternalServerError || response.Header().Get(requestIDHeader) != "request-id-generation-failed" ||
		strings.Contains(response.Body.String(), "entropy") || strings.Contains(response.Body.String(), "bad control") {
		t.Fatalf("random failure response: called=%v status=%d body=%s", called, response.Code, response.Body.String())
	}
}

// TestGeneratedRequestIDsAreConcurrentAndUnique 验证生产随机源下并发请求不会复用 ID。
func TestGeneratedRequestIDsAreConcurrentAndUnique(t *testing.T) {
	const workers = 32
	ids := make(chan string, workers)
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ids <- w.Header().Get(requestIDHeader) }))
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	wait.Wait()
	close(ids)
	seen := make(map[string]struct{}, workers)
	for id := range ids {
		if id == "" {
			t.Fatal("empty generated ID")
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request ID: %s", id)
		}
		seen[id] = struct{}{}
	}
}
