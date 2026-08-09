package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecutionRouteAllowlistRejectsProxyShapes(t *testing.T) {
	base := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions"
	tests := []struct {
		name, method, target, body string
	}{
		{name: "extra segment", method: http.MethodGet, target: base + "/exec_test/extra"},
		{name: "encoded slash", method: http.MethodGet, target: base + "/exec_test%2Flogs"},
		{name: "double encoded slash", method: http.MethodGet, target: base + "/exec_test%252Flogs"},
		{name: "unknown method", method: http.MethodPatch, target: base + "/exec_test"},
		{name: "status query socket", method: http.MethodGet, target: base + "/exec_test?socket=/var/run/docker.sock"},
		{name: "post query host", method: http.MethodPost, target: base + "?host=runner-elsewhere", body: `{"argv":["true"]}`},
		{name: "post body socket", method: http.MethodPost, target: base, body: `{"argv":["true"],"socket":"/var/run/docker.sock"}`},
		{name: "logs arbitrary path", method: http.MethodGet, target: base + "/exec_test/logs?path=/run/minisandbox/secret"},
		{name: "absolute form", method: http.MethodGet, target: "http://attacker.invalid" + base + "/exec_test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{}
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.method == http.MethodPost {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "text/event-stream")
			}
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
			if response.Code >= 200 && response.Code < 300 {
				t.Fatalf("proxy shape accepted: status=%d body=%s", response.Code, response.Body.String())
			}
			if len(service.commands)+len(service.statusCalls)+len(service.cancelCalls)+len(service.logCalls) != 0 {
				t.Fatalf("proxy shape reached application: %+v", service)
			}
		})
	}
}
