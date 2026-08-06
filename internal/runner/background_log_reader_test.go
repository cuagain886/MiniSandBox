package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestBackgroundLogReaderPaginatesBySequence 验证第一页、续页、重复 cursor、terminal 和完成后空页。
func TestBackgroundLogReaderPaginatesBySequence(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_reader_pages")
	events := logFixtureEvents(id)
	writeLogFixture(t, directory, id, events, true)
	reader, _ := NewBackgroundLogReader(directory, 2, 4096)
	first, err := reader.Read(id, 0)
	if err != nil || len(first.Events) != 2 || first.NextCursor != 2 || first.Complete {
		t.Fatalf("first: page=%+v err=%v", first, err)
	}
	repeated, err := reader.Read(id, 0)
	if err != nil || !equalLogPage(first, repeated) {
		t.Fatalf("repeat: page=%+v err=%v", repeated, err)
	}
	second, err := reader.Read(id, first.NextCursor)
	if err != nil || len(second.Events) != 1 || second.NextCursor != 3 || !second.Complete {
		t.Fatalf("second: page=%+v err=%v", second, err)
	}
	empty, err := reader.Read(id, second.NextCursor)
	if err != nil || len(empty.Events) != 0 || empty.NextCursor != 3 || !empty.Complete {
		t.Fatalf("empty: page=%+v err=%v", empty, err)
	}
}

// TestBackgroundLogReaderEnforcesResponseBytes 验证 page bytes 边界在事件数上限之前截页。
func TestBackgroundLogReaderEnforcesResponseBytes(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_reader_bytes")
	events := logFixtureEvents(id)
	writeLogFixture(t, directory, id, events, true)
	one := protocol.ExecutionLogPage{Events: []protocol.ExecutionEvent{events[0]}, NextCursor: 1, Complete: false}
	encoded, _ := json.Marshal(one)
	reader, err := NewBackgroundLogReader(directory, 10, int64(len(encoded)))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	page, err := reader.Read(id, 0)
	if err != nil || len(page.Events) != 1 || page.NextCursor != 1 {
		t.Fatalf("byte page: page=%+v err=%v", page, err)
	}
	actual, _ := json.Marshal(page)
	if len(actual) > len(encoded) {
		t.Fatalf("response exceeded limit: %d > %d", len(actual), len(encoded))
	}
}

// TestBackgroundLogReaderRejectsInvalidCursorCorruptionAndOversize 验证非法 cursor、损坏行、错序和超大行均受控失败。
func TestBackgroundLogReaderRejectsInvalidCursorCorruptionAndOversize(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_reader_invalid")
	events := logFixtureEvents(id)
	writeLogFixture(t, directory, id, events, true)
	reader, _ := NewBackgroundLogReader(directory, 10, 4096)
	if _, err := reader.Read(id, 4); !errors.Is(err, ErrInvalidLogCursor) {
		t.Fatalf("future cursor: %v", err)
	}
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "bad JSON", data: "{bad}\n"},
		{name: "gap", data: mustEventJSON(t, events[0]) + "\n" + mustEventJSON(t, withSequence(events[1], 3)) + "\n"},
		{name: "wrong ID", data: mustEventJSON(t, withExecutionID(events[0], "exec_other")) + "\n"},
		{name: "after terminal", data: mustEventJSON(t, events[0]) + "\n" + mustEventJSON(t, events[2]) + "\n" + mustEventJSON(t, events[1]) + "\n"},
		{name: "oversize", data: strings.Repeat("x", 5000) + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, _ := BackgroundLogPath(directory, id)
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatalf("write corrupt fixture: %v", err)
			}
			if _, err := reader.Read(id, 0); !errors.Is(err, ErrBackgroundLogCorrupt) {
				t.Fatalf("corrupt error: %v", err)
			}
		})
	}
}

// TestExecutionLogsHandlerMapsCursorPagesAndErrors 验证 HTTP cursor、分页、未知 ID 与损坏日志映射。
func TestExecutionLogsHandlerMapsCursorPagesAndErrors(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_logs_handler")
	events := logFixtureEvents(id)
	writeLogFixture(t, directory, id, events, true)
	manager := managerWithPendingID(t, id)
	reader, _ := NewBackgroundLogReader(directory, 2, 4096)
	handler, _ := NewExecutionLogsHandler(manager, reader)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, executionStatusPathPrefix+string(id)+executionLogsPathSuffix+"?cursor=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", response.Code, response.Body.String())
	}
	var page protocol.ExecutionLogPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil || len(page.Events) != 2 || page.NextCursor != 3 || !page.Complete {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, path := range []string{
		executionStatusPathPrefix + string(id) + executionLogsPathSuffix + "?cursor=-1",
		executionStatusPathPrefix + string(id) + executionLogsPathSuffix + "?cursor=01",
		executionStatusPathPrefix + string(id) + executionLogsPathSuffix + "?cursor=1&cursor=2",
	} {
		invalid := httptest.NewRecorder()
		handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, path, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor %s: %d", path, invalid.Code)
		}
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, executionStatusPathPrefix+"exec_unknown/logs", nil))
	if unknown.Code != http.StatusNotFound || strings.Contains(unknown.Body.String(), directory) {
		t.Fatalf("unknown: status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

// TestBackgroundLogReaderIgnoresIncompleteTailAndRejectsSymlink 验证并发 partial tail 暂不解析，且 reader 不跟随 symlink。
func TestBackgroundLogReaderIgnoresIncompleteTailAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_reader_tail")
	events := logFixtureEvents(id)
	path, _ := BackgroundLogPath(directory, id)
	data := mustEventJSON(t, events[0]) + "\n" + `{"execution_id":"exec_reader_tail"`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	reader, _ := NewBackgroundLogReader(directory, 10, 4096)
	page, err := reader.Read(id, 0)
	if err != nil || len(page.Events) != 1 || page.Complete {
		t.Fatalf("partial page=%+v err=%v", page, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	_ = os.WriteFile(target, []byte(mustEventJSON(t, events[0])+"\n"), 0o600)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := reader.Read(id, 0); !errors.Is(err, ErrBackgroundLogNotFound) {
		t.Fatalf("symlink read: %v", err)
	}
}

func logFixtureEvents(id ExecutionID) []protocol.ExecutionEvent {
	duration, truncated := int64(2), false
	base := protocol.ExecutionEvent{ExecutionID: string(id), Timestamp: time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)}
	started := base
	started.Sequence, started.Type = 1, protocol.EventStarted
	stdout := base
	stdout.Sequence, stdout.Type, stdout.DataBase64 = 2, protocol.EventStdout, "b2s="
	exited := base
	exitCode := 0
	exited.Sequence, exited.Type, exited.ExitCode, exited.DurationMS, exited.OutputTruncated = 3, protocol.EventExited, &exitCode, &duration, &truncated
	return []protocol.ExecutionEvent{started, stdout, exited}
}

func writeLogFixture(t *testing.T, directory string, id ExecutionID, events []protocol.ExecutionEvent, newline bool) {
	t.Helper()
	path, err := BackgroundLogPath(directory, id)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	var data strings.Builder
	for _, event := range events {
		data.WriteString(mustEventJSON(t, event))
		data.WriteByte('\n')
	}
	if !newline {
		value := data.String()
		data.Reset()
		data.WriteString(strings.TrimSuffix(value, "\n"))
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func mustEventJSON(t *testing.T, event protocol.ExecutionEvent) string {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(encoded)
}

func withSequence(event protocol.ExecutionEvent, sequence uint64) protocol.ExecutionEvent {
	event.Sequence = sequence
	return event
}
func withExecutionID(event protocol.ExecutionEvent, id string) protocol.ExecutionEvent {
	event.ExecutionID = id
	return event
}

func equalLogPage(left, right protocol.ExecutionLogPage) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func managerWithPendingID(t *testing.T, id ExecutionID) *Manager {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	return manager
}
