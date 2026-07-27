package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestReadinessDefaultAndCombinations 验证零值拒绝就绪且必须满足全部条件。
func TestReadinessDefaultAndCombinations(t *testing.T) {
	var readiness Readiness
	initial := readiness.Snapshot()
	if initial.Ready() {
		t.Fatal("zero-value readiness must not be ready")
	}
	if initial.Store ||
		initial.Docker ||
		initial.Artifact ||
		initial.Recovery ||
		initial.Worker {
		t.Fatalf("zero-value snapshot: %#v", initial)
	}

	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	if readiness.Snapshot().Ready() {
		t.Fatal("readiness must remain false before worker starts")
	}

	readiness.SetWorker(true)
	ready := readiness.Snapshot()
	if !ready.Ready() {
		t.Fatalf("all-ready snapshot: %#v", ready)
	}
	if initial.Ready() {
		t.Fatal("an earlier snapshot must remain immutable")
	}

	readiness.SetDocker(false)
	if readiness.Snapshot().Ready() {
		t.Fatal("losing one required component must make readiness false")
	}
}

// TestReadinessConcurrentAccess 验证各组件更新和快照读取可安全并发。
func TestReadinessConcurrentAccess(t *testing.T) {
	var readiness Readiness
	const iterations = 1_000
	var waitGroup sync.WaitGroup

	setters := []func(bool){
		readiness.SetStore,
		readiness.SetDocker,
		readiness.SetArtifact,
		readiness.SetRecovery,
		readiness.SetWorker,
	}
	for _, setter := range setters {
		setter := setter
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < iterations; index++ {
				setter(index%2 == 0)
			}
		}()
	}
	for reader := 0; reader < 5; reader++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < iterations; index++ {
				_ = readiness.Snapshot().Ready()
			}
		}()
	}
	waitGroup.Wait()

	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	readiness.SetWorker(true)
	if !readiness.Snapshot().Ready() {
		t.Fatal("readiness must remain usable after concurrent access")
	}
}

// TestReadinessEndpointStates 验证每个缺失条件返回 503，全部满足才返回 200。
func TestReadinessEndpointStates(t *testing.T) {
	setters := []struct {
		name string
		set  func(*Readiness, bool)
	}{
		{name: "store", set: (*Readiness).SetStore},
		{name: "docker", set: (*Readiness).SetDocker},
		{name: "artifact", set: (*Readiness).SetArtifact},
		{name: "recovery", set: (*Readiness).SetRecovery},
		{name: "worker", set: (*Readiness).SetWorker},
	}

	for missingIndex, missing := range setters {
		t.Run("missing "+missing.name, func(t *testing.T) {
			readiness := &Readiness{}
			for index, setter := range setters {
				setter.set(readiness, index != missingIndex)
			}
			response := requestReadiness(t, readiness)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf(
					"status: got %d, want %d",
					response.Code,
					http.StatusServiceUnavailable,
				)
			}
			payload := decodeReadinessResponse(t, response)
			if payload.Status != protocol.ReadinessStatusNotReady {
				t.Fatalf("readiness status: %q", payload.Status)
			}
			if payload.Components[missingIndex].Status !=
				protocol.ReadinessStatusNotReady {
				t.Fatalf("missing component: %#v", payload.Components[missingIndex])
			}
		})
	}

	t.Run("all ready", func(t *testing.T) {
		readiness := &Readiness{}
		for _, setter := range setters {
			setter.set(readiness, true)
		}
		response := requestReadiness(t, readiness)
		if response.Code != http.StatusOK {
			t.Fatalf("status: got %d, want %d", response.Code, http.StatusOK)
		}
		payload := decodeReadinessResponse(t, response)
		if payload.Status != protocol.ReadinessStatusReady {
			t.Fatalf("readiness status: %q", payload.Status)
		}
	})
}

// TestHealthUnaffectedByReadiness 验证依赖未就绪不会改变存活检查。
func TestHealthUnaffectedByReadiness(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	NewRouter(
		BuildInfo{Version: "test"},
		RouterDependencies{Readiness: &Readiness{}},
	).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("health status: got %d, want %d", response.Code, http.StatusOK)
	}
}

// requestReadiness 请求带指定状态对象的 readyz 路由。
func requestReadiness(
	t *testing.T,
	readiness *Readiness,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Header.Set(requestIDHeader, "req-ready")
	response := httptest.NewRecorder()
	NewRouter(
		BuildInfo{Version: "test"},
		RouterDependencies{Readiness: readiness},
	).ServeHTTP(response, request)
	if response.Header().Get(requestIDHeader) != "req-ready" {
		t.Fatalf(
			"request ID: got %q, want req-ready",
			response.Header().Get(requestIDHeader),
		)
	}
	return response
}

// decodeReadinessResponse 解码并检查固定五组件响应。
func decodeReadinessResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
) protocol.ReadinessResponse {
	t.Helper()
	var payload protocol.ReadinessResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if len(payload.Components) != 5 {
		t.Fatalf("component count: got %d, want 5", len(payload.Components))
	}
	return payload
}
