package runnerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestHealthContract 验证匹配版本成功，并对错误 service、旧版、未来版、
// label/health 不一致和非法 netns identity 全部 fail closed。
func TestHealthContract(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*protocol.RunnerHealth)
		expected int
		mismatch bool
		wantErr  bool
	}{
		{name: "matching", expected: runnerbootstrap.CurrentProtocolVersion},
		{name: "bad service", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.Service = "other" }, wantErr: true},
		{name: "old health protocol", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.ProtocolVersion = 0 }, mismatch: true, wantErr: true},
		{name: "future health protocol", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.ProtocolVersion = 2 }, mismatch: true, wantErr: true},
		{name: "forged label health inconsistency", expected: 2, mismatch: true, wantErr: true},
		{name: "invalid identity prefix", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.NetNSIdentity = "netns:4:99" }, wantErr: true},
		{name: "invalid identity zero", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.NetNSIdentity = "linux-netns:0:99" }, wantErr: true},
		{name: "invalid identity noncanonical", expected: 1, mutate: func(h *protocol.RunnerHealth) { h.NetNSIdentity = "linux-netns:04:99" }, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			health := protocol.RunnerHealth{Status: "ok", Service: "runnerd", Version: "test", ProtocolVersion: runnerbootstrap.CurrentProtocolVersion, NetNSIdentity: "linux-netns:4:99"}
			if test.mutate != nil {
				test.mutate(&health)
			}
			body, err := json.Marshal(health)
			if err != nil {
				t.Fatalf("marshal health: %v", err)
			}
			client := New("unused", "test-token")
			client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatalf("authorization header: %q", request.Header.Get("Authorization"))
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			got, err := client.Health(context.Background(), test.expected)
			if test.wantErr {
				if err == nil {
					t.Fatalf("invalid health accepted: %+v", got)
				}
				var mismatch *ProtocolMismatchError
				if test.mismatch != errors.As(err, &mismatch) {
					t.Fatalf("protocol mismatch classification: %v", err)
				}
				return
			}
			if err != nil || got != health {
				t.Fatalf("matching health rejected: got %+v, err %v", got, err)
			}
		})
	}
}

// TestHealthRejectsUnknownMissingAndTrailingFields 验证 health wire schema 封闭。
func TestHealthRejectsUnknownMissingAndTrailingFields(t *testing.T) {
	fixtures := []string{
		`{"status":"ok","service":"runnerd","version":"test","protocol_version":1,"netns_identity":"linux-netns:4:99","unknown":true}`,
		`{"status":"ok","service":"runnerd","version":"test","netns_identity":"linux-netns:4:99"}`,
		`{"status":"ok","service":"runnerd","version":"test","protocol_version":1,"netns_identity":"linux-netns:4:99"}{}`,
	}
	for _, fixture := range fixtures {
		client := New("unused", "test-token")
		client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fixture))}, nil
		})}
		_, err := client.Health(context.Background(), runnerbootstrap.CurrentProtocolVersion)
		if err == nil {
			t.Fatalf("invalid fixture accepted: %s", fixture)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestHealthRejectsEmptyToken 验证 runnerclient 不会发出未鉴权的内部请求。
func TestHealthRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", " \t"} {
		client := New("unused", token)
		client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("empty-token request reached transport")
			return nil, nil
		})}
		if _, err := client.Health(context.Background(), runnerbootstrap.CurrentProtocolVersion); err == nil {
			t.Fatalf("empty runner token accepted: %q", token)
		}
	}
}
