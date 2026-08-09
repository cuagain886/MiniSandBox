//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestPublicBackgroundLogsCursorContract 验证公共 API 的多页、重放、终态空页和非法 cursor 语义。
func TestPublicBackgroundLogsCursorContract(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		return strings.Replace(content, "runner:\n  execution_uid: 65532\n  execution_gid: 65532", "runner:\n  execution_uid: 65532\n  execution_gid: 65532\n  max_log_page_events: 8", 1)
	})
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	descriptor := postPublicBackground(t, t.Context(), instance.baseURL, sandboxID, protocol.ExecuteRequest{
		Argv: []string{executionHelperPath, "log-pages", "8"}, Background: true,
	})
	status := waitPublicExecutionTerminal(t, instance.baseURL, sandboxID, descriptor.ExecutionID)

	var all []protocol.ExecutionEvent
	cursor := uint64(0)
	pageCount := 0
	for {
		page := getPublicExecutionLogPage(t, instance.baseURL, sandboxID, descriptor.ExecutionID, cursor, "2")
		if pageCount == 0 {
			repeated := getPublicExecutionLogPage(t, instance.baseURL, sandboxID, descriptor.ExecutionID, cursor, "2")
			if !reflect.DeepEqual(page, repeated) {
				t.Fatalf("repeated cursor changed page: first=%+v repeated=%+v", page, repeated)
			}
		}
		if len(page.Events) == 0 || len(page.Events) > 2 || page.NextCursor <= cursor {
			t.Fatalf("invalid page %d: cursor=%d page=%+v", pageCount, cursor, page)
		}
		all = append(all, page.Events...)
		cursor = page.NextCursor
		pageCount++
		if page.Complete {
			break
		}
	}
	if pageCount < 2 || len(all) < 4 {
		t.Fatalf("background log did not paginate: pages=%d events=%d", pageCount, len(all))
	}
	terminal := all[len(all)-1]
	if status.TerminalEvent == nil || !reflect.DeepEqual(terminal, *status.TerminalEvent) || status.ExecutionID != descriptor.ExecutionID {
		t.Fatalf("status/log terminal mismatch: status=%+v terminal=%+v", status, terminal)
	}
	empty := getPublicExecutionLogPage(t, instance.baseURL, sandboxID, descriptor.ExecutionID, cursor, "2")
	if len(empty.Events) != 0 || empty.NextCursor != cursor || !empty.Complete {
		t.Fatalf("terminal cursor page: %+v", empty)
	}
	path := publicExecutionLogsURL(instance.baseURL, sandboxID, descriptor.ExecutionID) + "?cursor=01&limit=2"
	response, err := http.Get(path)
	if err != nil {
		t.Fatalf("get invalid cursor: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor status: got %d, want 400", response.StatusCode)
	}
}

func getPublicExecutionLogPage(t *testing.T, baseURL, sandboxID, executionID string, cursor uint64, limit string) protocol.ExecutionLogPage {
	t.Helper()
	path := fmt.Sprintf("%s?cursor=%d&limit=%s", publicExecutionLogsURL(baseURL, sandboxID, executionID), cursor, limit)
	response, err := http.Get(path)
	if err != nil {
		t.Fatalf("get public execution logs: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public execution logs status: %d", response.StatusCode)
	}
	var page protocol.ExecutionLogPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decode public execution logs: %v", err)
	}
	return page
}

func publicExecutionLogsURL(baseURL, sandboxID, executionID string) string {
	return baseURL + "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/executions/" + url.PathEscape(executionID) + "/logs"
}
