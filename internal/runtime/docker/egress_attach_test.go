package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
	"minisandbox/internal/egresspolicy"
)

// TestEgressControlSessionExchange 验证 adapter 在 Docker multiplex stdout 上只接受
// 与当前 request ID/nonce 匹配的一条有界 attestation，且不会关闭容器 stdin。
func TestEgressControlSessionExchange(t *testing.T) {
	request := egresscontrol.Request{
		Type: egresscontrol.RequestInspect, RequestID: "00112233445566778899aabbccddeeff",
		Nonce: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
	}
	attestation := attachTestAttestation(t)
	response, err := egresscontrol.EncodeResponse(egresscontrol.Response{
		RequestID: request.RequestID, Nonce: request.Nonce, Attestation: attestation,
	})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	connection := &recordingConn{}
	session := &egressControlSession{
		attached: mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{
			Conn: connection, Reader: bufio.NewReader(bytes.NewReader(dockerStdoutFrame(1, response))),
		}},
	}
	session.stdout = &dockerStdoutReader{reader: session.attached.Reader}
	got, err := session.exchange(request)
	if err != nil || got != attestation {
		t.Fatalf("exchange control request: got=%+v err=%v", got, err)
	}
	if connection.writeClosed {
		t.Fatal("reconnectable container stdin was closed")
	}
	decoded, err := egresscontrol.ReadRequest(bytes.NewReader(connection.data.Bytes()))
	if err != nil || decoded.Type != egresscontrol.RequestInspect || decoded.Nonce != request.Nonce {
		t.Fatalf("decode written request: request=%+v err=%v", decoded, err)
	}
}

// TestEgressControlSessionRejectsReplay 验证格式合法但属于其他 nonce 的历史响应
// 不能被当前 attach 请求接受。
func TestEgressControlSessionRejectsReplay(t *testing.T) {
	request := egresscontrol.Request{
		Type: egresscontrol.RequestInspect, RequestID: "00112233445566778899aabbccddeeff",
		Nonce: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
	}
	response, err := egresscontrol.EncodeResponse(egresscontrol.Response{
		RequestID: request.RequestID, Nonce: strings.Repeat("f", 64), Attestation: attachTestAttestation(t),
	})
	if err != nil {
		t.Fatalf("encode replay response: %v", err)
	}
	connection := &recordingConn{}
	session := &egressControlSession{
		attached: mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{
			Conn: connection, Reader: bufio.NewReader(bytes.NewReader(dockerStdoutFrame(1, response))),
		}},
	}
	session.stdout = &dockerStdoutReader{reader: session.attached.Reader}
	if _, err := session.exchange(request); err == nil {
		t.Fatal("replayed response accepted")
	}
}

// TestDockerStdoutReaderHandlesFragmentedFrames 验证一个控制响应可以跨多个 Docker
// stdout frame，同时 stderr、保留位和超限 frame 都会被拒绝。
func TestDockerStdoutReaderHandlesFragmentedFrames(t *testing.T) {
	payload := []byte("abcdefgh")
	stream := append(dockerStdoutFrame(1, payload[:3]), dockerStdoutFrame(1, payload[3:])...)
	reader := &dockerStdoutReader{reader: bufio.NewReader(bytes.NewReader(stream))}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read fragmented stdout: got=%q err=%v", got, err)
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "stderr", data: dockerStdoutFrame(2, []byte("failure"))},
		{name: "reserved bits", data: func() []byte { data := dockerStdoutFrame(1, []byte("x")); data[1] = 1; return data }()},
		{name: "oversized", data: dockerHeader(1, maxDockerEgressPayload+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := &dockerStdoutReader{reader: bufio.NewReader(bytes.NewReader(test.data))}
			if _, err := candidate.Read(make([]byte, 1)); err == nil {
				t.Fatal("invalid Docker stream frame accepted")
			}
		})
	}
}

// TestOpenEgressControlSessionOptions 锁定 attach 只启用当前容器 stdin/stdout，关闭
// logs/stderr/TTY 依赖，并把 context 取消映射为连接关闭。
func TestOpenEgressControlSessionOptions(t *testing.T) {
	connection := newSignalingConn()
	engine := &fakeEngine{containerAttachFunc: func(_ context.Context, id string, options mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
		if id != "sidecar-id" || !options.Stream || !options.Stdin || !options.Stdout || options.Stderr || options.Logs {
			t.Fatalf("unsafe attach options: %+v", options)
		}
		return mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{
			Conn: connection, Reader: bufio.NewReader(bytes.NewReader(nil)),
		}}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := openEgressControlSession(ctx, engine, "sidecar-id", time.Second)
	if err != nil {
		t.Fatalf("open control session: %v", err)
	}
	cancel()
	select {
	case <-connection.closedSignal:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close attach connection")
	}
	session.close()
}

func attachTestAttestation(t *testing.T) egressanchor.Attestation {
	t.Helper()
	policy, err := egresspolicy.Build(nil, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return egressanchor.Attestation{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, NetworkNamespace: "linux-netns:4:4026533000",
		ImageDigest: "registry.example/egressd@sha256:" + strings.Repeat("a", 64),
		CreatedAt:   time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
}

func dockerStdoutFrame(stream byte, payload []byte) []byte {
	result := dockerHeader(stream, uint32(len(payload)))
	return append(result, payload...)
}

func dockerHeader(stream byte, length uint32) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], length)
	return header
}

type signalingConn struct {
	recordingConn
	closedSignal chan struct{}
	closeOnce    sync.Once
}

func newSignalingConn() *signalingConn {
	return &signalingConn{closedSignal: make(chan struct{})}
}

func (connection *signalingConn) Close() error {
	err := connection.recordingConn.Close()
	connection.closeOnce.Do(func() { close(connection.closedSignal) })
	return err
}
