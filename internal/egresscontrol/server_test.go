package egresscontrol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egressnft"
)

// TestServeBootstrapThenInspect 验证 server 只安装一次 nft，初次响应后继续通过新
// 请求关联字段返回同一内存 attestation，并在取消时正常结束。
func TestServeBootstrapThenInspect(t *testing.T) {
	bootstrap := testBootstrap(t)
	first := requestFrame(t, Request{Type: RequestBootstrap, RequestID: testRequestID, Nonce: testNonce, Bootstrap: &bootstrap})
	secondID, secondNonce := strings.Repeat("a", 32), strings.Repeat("b", 64)
	second := requestFrame(t, Request{Type: RequestInspect, RequestID: secondID, Nonce: secondNonce})
	ctx, cancel := context.WithCancel(context.Background())
	output := &cancelWriter{cancel: cancel, maximumWrites: 2}
	executor := &recordingExecutor{}
	platform := validServerPlatform(bootstrap)
	err := Serve(ctx, io.NopCloser(bytes.NewReader(append(first, second...))), output, ServerOptions{
		Executor: executor, Platform: platform,
		Now: func() time.Time { return time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("serve control stream: %v", err)
	}
	if executor.installCalls != 1 || executor.readbackCalls != 1 || platform.dropCalls != 1 || platform.snapshotCalls != 2 {
		t.Fatalf("unexpected bootstrap/inspect calls: executor=%+v platform=%+v", executor, platform)
	}
	responses := bytes.NewReader(output.Bytes())
	ready, err := ReadResponse(responses)
	if err != nil || ready.RequestID != testRequestID || ready.Nonce != testNonce {
		t.Fatalf("read ready response: response=%+v err=%v", ready, err)
	}
	healthy, err := ReadResponse(responses)
	if err != nil || healthy.RequestID != secondID || healthy.Nonce != secondNonce ||
		healthy.Attestation != ready.Attestation {
		t.Fatalf("read inspect response: response=%+v err=%v", healthy, err)
	}
}

// TestServeRejectsStateMachineViolations 验证 inspect-before-bootstrap 与第二次
// bootstrap 都会使 sidecar 状态机 fail closed。
func TestServeRejectsStateMachineViolations(t *testing.T) {
	bootstrap := testBootstrap(t)
	bootstrapFrame := requestFrame(t, Request{Type: RequestBootstrap, RequestID: testRequestID, Nonce: testNonce, Bootstrap: &bootstrap})
	inspectFrame := requestFrame(t, Request{Type: RequestInspect, RequestID: testRequestID, Nonce: testNonce})
	for _, test := range []struct {
		name  string
		input []byte
	}{
		{name: "inspect before bootstrap", input: inspectFrame},
		{name: "duplicate bootstrap", input: append(append([]byte(nil), bootstrapFrame...), bootstrapFrame...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Serve(context.Background(), io.NopCloser(bytes.NewReader(test.input)), io.Discard, ServerOptions{
				Executor: &recordingExecutor{}, Platform: validServerPlatform(bootstrap),
			}); err == nil {
				t.Fatal("state machine violation accepted")
			}
		})
	}
}

// TestServeRejectsPrivilegeDrift 验证 inspect 会重新读取权限，发现重新出现的
// NET_ADMIN 后不发送第二条 attestation 并直接结束 sidecar。
func TestServeRejectsPrivilegeDrift(t *testing.T) {
	bootstrap := testBootstrap(t)
	first := requestFrame(t, Request{Type: RequestBootstrap, RequestID: testRequestID, Nonce: testNonce, Bootstrap: &bootstrap})
	second := requestFrame(t, Request{Type: RequestInspect, RequestID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 64)})
	platform := validServerPlatform(bootstrap)
	platform.driftAfterSnapshot = 1
	var output bytes.Buffer
	err := Serve(context.Background(), io.NopCloser(bytes.NewReader(append(first, second...))), &output, ServerOptions{
		Executor: &recordingExecutor{}, Platform: platform,
	})
	if err == nil {
		t.Fatal("privilege drift accepted")
	}
	reader := bytes.NewReader(output.Bytes())
	if _, err := ReadResponse(reader); err != nil {
		t.Fatalf("initial ready response missing: %v", err)
	}
	if reader.Len() != 0 {
		t.Fatal("inspect emitted attestation after privilege drift")
	}
}

func requestFrame(t *testing.T, request Request) []byte {
	t.Helper()
	encoded, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return encoded
}

type recordingExecutor struct {
	rules         []byte
	installCalls  int
	readbackCalls int
}

func (executor *recordingExecutor) Run(_ context.Context, _ string, args []string, data []byte) ([]byte, error) {
	if len(args) == 2 && args[0] == "-f" && args[1] == "-" {
		executor.installCalls++
		executor.rules = append([]byte(nil), data...)
		return nil, nil
	}
	if len(args) == 4 && strings.Join(args, " ") == "list table inet minisandbox_egress" {
		executor.readbackCalls++
		return append([]byte(nil), executor.rules...), nil
	}
	return nil, errors.New("unexpected nft invocation")
}

type serverPlatform struct {
	networkNamespace   string
	snapshot           egressanchor.Snapshot
	dropCalls          int
	snapshotCalls      int
	driftAfterSnapshot int
}

func validServerPlatform(bootstrap egressnft.Bootstrap) *serverPlatform {
	return &serverPlatform{
		networkNamespace: bootstrap.NetworkNamespace,
		snapshot: egressanchor.Snapshot{
			UID: bootstrap.AnchorUID, GID: bootstrap.AnchorGID,
			SupplementaryGroups: []uint32{bootstrap.AnchorGID}, NoNewPrivileges: true,
		},
	}
}

func (platform *serverPlatform) NetworkNamespace() (string, error) {
	return platform.networkNamespace, nil
}

func (platform *serverPlatform) DropPrivileges(uint32, uint32) error {
	platform.dropCalls++
	return nil
}

func (platform *serverPlatform) Snapshot() (egressanchor.Snapshot, error) {
	platform.snapshotCalls++
	snapshot := platform.snapshot
	if platform.driftAfterSnapshot > 0 && platform.snapshotCalls > platform.driftAfterSnapshot {
		snapshot.CapEffective = uint64(1) << 12
	}
	return snapshot, nil
}

type cancelWriter struct {
	bytes.Buffer
	cancel        context.CancelFunc
	maximumWrites int
	writes        int
}

func (writer *cancelWriter) Write(data []byte) (int, error) {
	written, err := writer.Buffer.Write(data)
	writer.writes++
	if writer.writes >= writer.maximumWrites {
		writer.cancel()
	}
	return written, err
}
