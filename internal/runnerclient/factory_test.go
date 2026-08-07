package runnerclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

const factorySandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

func TestFactoryDerivesAuthorizationAndCachesSuccessfulHealth(t *testing.T) {
	key := testFactoryMasterKey()
	factory, err := NewFactory(t.TempDir(), &key, runnerbootstrap.CurrentProtocolVersion, time.Second)
	if err != nil {
		t.Fatalf("new factory: %v", err)
	}
	defer factory.Close()
	client, err := factory.Client(factorySandboxID)
	if err != nil {
		t.Fatalf("factory client: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	token, err := runnerauth.DeriveToken(&key, factorySandboxID)
	if err != nil {
		t.Fatalf("derive expected token: %v", err)
	}
	wantAuthorization := "Bearer " + base64.RawURLEncoding.EncodeToString(token[:])
	token.Clear()
	healthCalls, statusCalls := 0, 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != wantAuthorization {
			t.Fatalf("authorization mismatch")
		}
		switch request.URL.Path {
		case "/healthz":
			healthCalls++
			return jsonResponse(http.StatusOK, protocol.RunnerHealth{Status: "ok", Service: "runnerd", Version: "build", ProtocolVersion: runnerbootstrap.CurrentProtocolVersion, NetNSIdentity: "linux-netns:4:99"}), nil
		case "/v1/executions/exec_test":
			statusCalls++
			return jsonResponse(http.StatusOK, protocol.ExecutionStatus{ExecutionID: "exec_test", State: protocol.ExecutionStateRunning}), nil
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
			return nil, nil
		}
	})}
	for index := 0; index < 2; index++ {
		if _, err := client.Status(context.Background(), "exec_test"); err != nil {
			t.Fatalf("cached status %d: %v", index, err)
		}
	}
	if healthCalls != 1 || statusCalls != 2 {
		t.Fatalf("cached calls: health=%d status=%d", healthCalls, statusCalls)
	}
	now = now.Add(2 * time.Second)
	if _, err := client.Status(context.Background(), "exec_test"); err != nil {
		t.Fatalf("status after expiry: %v", err)
	}
	if healthCalls != 2 || statusCalls != 3 {
		t.Fatalf("expired calls: health=%d status=%d", healthCalls, statusCalls)
	}
}

func TestHealthGateBlocksExecutionOnProtocolAndAuthFailures(t *testing.T) {
	tests := []struct {
		name       string
		healthCode int
		version    int
		want       any
	}{
		{name: "protocol", healthCode: http.StatusOK, version: runnerbootstrap.CurrentProtocolVersion + 1, want: &ProtocolMismatchError{}},
		{name: "authentication", healthCode: http.StatusUnauthorized, version: runnerbootstrap.CurrentProtocolVersion, want: &AuthenticationError{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := testFactoryMasterKey()
			factory, err := NewFactory(t.TempDir(), &key, runnerbootstrap.CurrentProtocolVersion, time.Second)
			if err != nil {
				t.Fatalf("new factory: %v", err)
			}
			defer factory.Close()
			client, err := factory.Client(factorySandboxID)
			if err != nil {
				t.Fatalf("factory client: %v", err)
			}
			executionSent := false
			client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/healthz" {
					executionSent = true
				}
				if test.healthCode != http.StatusOK {
					return &http.Response{StatusCode: test.healthCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return jsonResponse(http.StatusOK, protocol.RunnerHealth{Status: "ok", Service: "runnerd", Version: "build", ProtocolVersion: test.version, NetNSIdentity: "linux-netns:4:99"}), nil
			})}
			_, err = client.Status(context.Background(), "exec_test")
			switch test.want.(type) {
			case *ProtocolMismatchError:
				var target *ProtocolMismatchError
				if !errors.As(err, &target) {
					t.Fatalf("classification: %v", err)
				}
			case *AuthenticationError:
				var target *AuthenticationError
				if !errors.As(err, &target) {
					t.Fatalf("classification: %v", err)
				}
			}
			if executionSent {
				t.Fatal("execution request sent before health gate")
			}
		})
	}
}

func TestFactoryAndClientFormattingRedactsSecrets(t *testing.T) {
	key := testFactoryMasterKey()
	factory, err := NewFactory(filepath.Join(t.TempDir(), "sensitive-root"), &key, runnerbootstrap.CurrentProtocolVersion, time.Second)
	if err != nil {
		t.Fatalf("new factory: %v", err)
	}
	client, err := factory.Client(factorySandboxID)
	if err != nil {
		t.Fatalf("factory client: %v", err)
	}
	formatted := fmt.Sprintf("%v %#v %v %#v", factory, factory, client, client)
	if strings.Contains(formatted, "sensitive-root") || strings.Contains(formatted, base64.RawURLEncoding.EncodeToString(key[:])) {
		t.Fatalf("secret leaked through formatting: %s", formatted)
	}
	factory.Close()
	if _, err := client.Status(context.Background(), "exec_test"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("closed factory error is unsafe: %v", err)
	}
}

func TestHealthGateClassifiesConnectionFailureAndDoesNotCacheIt(t *testing.T) {
	key := testFactoryMasterKey()
	factory, err := NewFactory(t.TempDir(), &key, runnerbootstrap.CurrentProtocolVersion, time.Second)
	if err != nil {
		t.Fatalf("new factory: %v", err)
	}
	defer factory.Close()
	client, err := factory.Client(factorySandboxID)
	if err != nil {
		t.Fatalf("factory client: %v", err)
	}
	calls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("socket unavailable")
	})}
	for range 2 {
		_, err := client.Status(context.Background(), "exec_test")
		var connection *ConnectionError
		if !errors.As(err, &connection) {
			t.Fatalf("connection classification: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("failed health was cached: calls=%d", calls)
	}
}

func testFactoryMasterKey() runnerauth.MasterKey {
	var key runnerauth.MasterKey
	for index := range key {
		key[index] = byte(index + 1)
	}
	return key
}
