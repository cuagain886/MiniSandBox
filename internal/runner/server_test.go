package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestHealthReturnsProtocolAndNetNSIdentity 验证 health 返回固定 service、精确
// 协议版本和从可信 reader 获得的当前 netns identity。
func TestHealthReturnsProtocolAndNetNSIdentity(t *testing.T) {
	handler := newServer("test-build", "", func() (string, error) {
		return "linux-netns:4:4026532000", nil
	})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d", response.Code)
	}
	var got protocol.RunnerHealth
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Status != "ok" || got.Service != "runnerd" || got.Version != "test-build" || got.ProtocolVersion != runnerbootstrap.CurrentProtocolVersion || got.NetNSIdentity != "linux-netns:4:4026532000" {
		t.Fatalf("unexpected health: %+v", got)
	}
}

// TestHealthFailsClosedWhenNetNSUnavailable 验证 stat/格式读取失败不会返回就绪。
func TestHealthFailsClosedWhenNetNSUnavailable(t *testing.T) {
	handler := newServer("test-build", "", func() (string, error) {
		return "", errors.New("stat failed")
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

// TestFormatNetNSIdentity 验证固定格式并拒绝无效 stat identity。
func TestFormatNetNSIdentity(t *testing.T) {
	if got, err := formatNetNSIdentity(4, 99); err != nil || got != "linux-netns:4:99" {
		t.Fatalf("format identity: got %q, err %v", got, err)
	}
	for _, input := range [][2]uint64{{0, 1}, {1, 0}} {
		if _, err := formatNetNSIdentity(input[0], input[1]); err == nil {
			t.Fatalf("invalid stat identity accepted: %+v", input)
		}
	}
}
