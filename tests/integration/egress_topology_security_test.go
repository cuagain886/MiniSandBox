//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/pkg/protocol"
)

const integrationEgressImageEnv = "MINISANDBOX_TEST_EGRESS_IMAGE"

// TestEgressSidecarTopologyAndLeastPrivilege 验证 outbound 只增加共享 netns 的最小权限 sidecar。
func TestEgressSidecarTopologyAndLeastPrivilege(t *testing.T) {
	egressImage := os.Getenv(integrationEgressImageEnv)
	if egressImage == "" || !strings.Contains(egressImage, "@sha256:") {
		t.Skip("set MINISANDBOX_TEST_EGRESS_IMAGE to a locally available digest reference")
	}
	harness := newDockerHarness(t)
	info, err := harness.client.Info(t.Context(), mobyclient.InfoOptions{})
	if err == nil && strings.Contains(strings.ToLower(info.Info.OperatingSystem), "docker desktop") {
		t.Skip("native Linux Docker is required for host netns inode attestation")
	}
	image := integrationImage()
	harness.ensureImage(t, image)
	harness.ensureImage(t, egressImage)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		content = strings.Replace(content, "allow_outbound: false", "allow_outbound: true", 1)
		return strings.Replace(content, "runner:\n", "egress:\n  image: "+strconv.Quote(egressImage)+"\n  ready_timeout: \"15s\"\nrunner:\n", 1)
	})

	networkNone := createSandboxWithNetwork(t, instance.baseURL, image, false)
	harness.trackSandbox(networkNone.ID)
	waitSandboxState(t, instance.baseURL, networkNone.ID, protocol.SandboxStateRunning)
	noneContainerID := harness.runningContainerID(t, networkNone.ID)
	noneInspect := inspectContainer(t, harness.client, noneContainerID)
	if noneInspect.Config == nil || noneInspect.HostConfig == nil || !noneInspect.Config.NetworkDisabled || noneInspect.HostConfig.NetworkMode != "none" {
		t.Fatalf("network-none topology drifted")
	}
	if _, err := harness.client.ContainerInspect(t.Context(), "minisandbox-egress-"+networkNone.ID, mobyclient.ContainerInspectOptions{}); err == nil {
		t.Fatal("network-none sandbox created an egress sidecar")
	}

	outbound := createSandboxWithNetwork(t, instance.baseURL, image, true)
	harness.trackSandbox(outbound.ID)
	waitSandboxState(t, instance.baseURL, outbound.ID, protocol.SandboxStateRunning)
	mainID := harness.runningContainerID(t, outbound.ID)
	sidecarName := "minisandbox-egress-" + outbound.ID
	mainInspect := inspectContainer(t, harness.client, mainID)
	sidecar := inspectContainer(t, harness.client, sidecarName)
	if mainInspect.HostConfig == nil || mainInspect.Config == nil || sidecar.HostConfig == nil || sidecar.Config == nil || sidecar.State == nil {
		t.Fatal("egress topology inspect is incomplete")
	}
	if mainInspect.Config.NetworkDisabled || mainInspect.HostConfig.NetworkMode != mobycontainer.NetworkMode("container:"+sidecar.ID) {
		t.Fatalf("main container does not share the sidecar netns")
	}
	assertCapabilitySet(t, "main CapAdd", mainInspect.HostConfig.CapAdd, []string{"CHOWN", "SETUID", "SETGID", "KILL"})
	for _, capability := range mainInspect.HostConfig.CapAdd {
		if strings.Contains(strings.ToUpper(capability), "NET_ADMIN") || strings.Contains(strings.ToUpper(capability), "NET_RAW") {
			t.Fatal("main sandbox received a network capability")
		}
	}
	if len(mainInspect.HostConfig.PortBindings) != 0 || len(mainInspect.Config.ExposedPorts) != 0 {
		t.Fatal("main sandbox exposes a network port")
	}

	if sidecar.Config.Image != egressImage || sidecar.Config.User != "0:0" || sidecar.Config.Labels[dockerruntime.LabelSandboxID] != outbound.ID ||
		sidecar.Config.Labels[dockerruntime.LabelResourceRole] != "egress-sidecar" || sidecar.Config.Labels[dockerruntime.LabelEgressImage] != egressImage {
		t.Fatal("sidecar immutable identity or labels drifted")
	}
	if sidecar.HostConfig.Privileged || !sidecar.HostConfig.ReadonlyRootfs || sidecar.HostConfig.RestartPolicy.Name != mobycontainer.RestartPolicyDisabled ||
		sidecar.HostConfig.LogConfig.Type != "none" || len(sidecar.HostConfig.Tmpfs) != 0 ||
		!sidecar.Config.AttachStdin || !sidecar.Config.AttachStdout || sidecar.Config.AttachStderr || !sidecar.Config.OpenStdin || sidecar.Config.StdinOnce || sidecar.Config.Tty ||
		sidecar.Config.WorkingDir != "/" || len(sidecar.Config.Env) != 1 || sidecar.Config.Env[0] != "PATH=/usr/sbin:/usr/bin:/sbin:/bin" ||
		len(sidecar.Config.Cmd) != 0 || len(sidecar.Config.ExposedPorts) != 0 || len(sidecar.HostConfig.PortBindings) != 0 ||
		len(sidecar.HostConfig.Binds) != 0 || len(sidecar.HostConfig.Mounts) != 0 || len(sidecar.HostConfig.Devices) != 0 || len(sidecar.HostConfig.VolumesFrom) != 0 {
		t.Fatal("sidecar least-privilege profile drifted")
	}
	if sidecar.HostConfig.Memory <= 0 || sidecar.HostConfig.MemorySwap != sidecar.HostConfig.Memory {
		t.Fatal("sidecar memory+swap boundary drifted")
	}
	assertCapabilitySet(t, "sidecar CapDrop", sidecar.HostConfig.CapDrop, []string{"ALL"})
	assertCapabilitySet(t, "sidecar CapAdd", sidecar.HostConfig.CapAdd, []string{"NET_ADMIN", "SETUID", "SETGID"})
	if !containsSecurityOption(sidecar.HostConfig.SecurityOpt, "no-new-privileges:true") || sidecar.State.Pid <= 1 {
		t.Fatal("sidecar security option or PID is invalid")
	}
	statusContent, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", sidecar.State.Pid))
	if err != nil {
		t.Fatalf("read sidecar anchor status: %v", err)
	}
	statusFields := parseStatusFields(statusContent)
	assertIdentityAndCapabilities(t, statusFields, 65532, 65532)
	groups := statusFields["Groups"]
	if len(groups) > 1 || len(groups) == 1 && groups[0] != "65532" {
		t.Fatalf("sidecar anchor has additional group privileges: %v", groups)
	}
	for _, field := range []string{"CapEff", "CapPrm", "CapAmb"} {
		if len(statusFields[field]) != 1 || statusFields[field][0] != "0000000000000000" {
			t.Fatalf("sidecar anchor %s: %v", field, statusFields[field])
		}
	}
	if values := statusFields["NoNewPrivs"]; len(values) != 1 || values[0] != "1" {
		t.Fatalf("sidecar anchor NoNewPrivs: %v", values)
	}

	attestation := inspectSidecarAttestation(t, harness.client, sidecar.ID)
	health, err := instance.runnerClient(t, outbound.ID).Health(t.Context(), 1)
	if err != nil || health.NetNSIdentity != attestation.NetworkNamespace || attestation.ImageDigest != egressImage || attestation.PolicyHash != sidecar.Config.Labels[dockerruntime.LabelEgressPolicyHash] {
		t.Fatalf("netns/image/policy attestation mismatch: health=%+v attestation=%+v err=%v", health, attestation, err)
	}

	network, err := harness.client.NetworkInspect(t.Context(), dockerruntime.EgressNetworkName, mobyclient.NetworkInspectOptions{})
	if err != nil || network.Network.Driver != "bridge" || network.Network.Internal || network.Network.Labels[dockerruntime.LabelResourceRole] != "egress-network" {
		t.Fatalf("managed egress bridge: network=%+v err=%v", network.Network, err)
	}
	if submitSandboxDelete(t, instance.baseURL, networkNone.ID) != http.StatusAccepted {
		t.Fatal("delete network-none sandbox")
	}
	waitSandboxState(t, instance.baseURL, networkNone.ID, protocol.SandboxStateTerminated)
	if _, err := harness.client.NetworkInspect(t.Context(), dockerruntime.EgressNetworkName, mobyclient.NetworkInspectOptions{}); err != nil {
		t.Fatal("ordinary sandbox deletion removed the service egress bridge")
	}
}

// TestOutboundSandboxIsDeniedByDefault 验证服务端未授权时请求不能触发 sidecar 或 bridge 创建。
func TestOutboundSandboxIsDeniedByDefault(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	body, _ := json.Marshal(protocol.CreateSandboxRequest{Image: image, Network: &protocol.SandboxNetworkRequest{Outbound: true}})
	response, err := http.Post(instance.baseURL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post denied outbound sandbox: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	var envelope protocol.ErrorResponse
	if response.StatusCode != http.StatusForbidden || json.Unmarshal(raw, &envelope) != nil || envelope.Error.Code != string(protocol.ErrorCodeOutboundNotAllowed) {
		t.Fatalf("outbound denial: status=%d code=%q", response.StatusCode, envelope.Error.Code)
	}
}

func createSandboxWithNetwork(t *testing.T, baseURL, image string, outbound bool) protocol.Sandbox {
	t.Helper()
	body, err := json.Marshal(protocol.CreateSandboxRequest{Image: image, Network: &protocol.SandboxNetworkRequest{Outbound: outbound}})
	if err != nil {
		t.Fatalf("encode sandbox network request: %v", err)
	}
	response, err := http.Post(baseURL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post sandbox network request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("sandbox network response: status=%d body=%s", response.StatusCode, raw)
	}
	var sandbox protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&sandbox); err != nil {
		t.Fatalf("decode sandbox network response: %v", err)
	}
	return sandbox
}

func inspectContainer(t *testing.T, client *mobyclient.Client, name string) mobycontainer.InspectResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.ContainerInspect(ctx, name, mobyclient.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect container: %v", err)
	}
	return result.Container
}

func inspectSidecarAttestation(t *testing.T, client *mobyclient.Client, containerID string) egressanchor.Attestation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	attached, err := client.ContainerAttach(ctx, containerID, mobyclient.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true,
	})
	if err != nil {
		t.Fatalf("attach sidecar inspect: %v", err)
	}
	defer attached.Close()
	if attached.Conn == nil || attached.Reader == nil {
		t.Fatal("sidecar inspect attach is incomplete")
	}
	if err := attached.Conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set sidecar inspect deadline: %v", err)
	}
	requestID, nonce, err := egresscontrol.NewCorrelation()
	if err != nil {
		t.Fatalf("generate sidecar inspect correlation: %v", err)
	}
	request := egresscontrol.Request{Type: egresscontrol.RequestInspect, RequestID: requestID, Nonce: nonce}
	framed, err := egresscontrol.EncodeRequest(request)
	if err != nil {
		t.Fatalf("encode sidecar inspect: %v", err)
	}
	for len(framed) > 0 {
		written, err := attached.Conn.Write(framed)
		if err != nil || written <= 0 {
			t.Fatalf("write sidecar inspect: written=%d err=%v", written, err)
		}
		framed = framed[written:]
	}
	response, err := egresscontrol.ReadResponse(&integrationDockerStdoutReader{reader: attached.Reader})
	if err != nil {
		t.Fatalf("read sidecar inspect: %v", err)
	}
	if response.RequestID != requestID || response.Nonce != nonce {
		t.Fatal("sidecar inspect correlation mismatch")
	}
	return response.Attestation
}

type integrationDockerStdoutReader struct {
	reader    *bufio.Reader
	remaining uint32
}

func (reader *integrationDockerStdoutReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if reader.remaining == 0 {
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader.reader, header); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[4:])
		if header[0] != 1 || header[1] != 0 || header[2] != 0 || header[3] != 0 ||
			length == 0 || length > egresscontrol.MaxResponseBytes+4 {
			return 0, errors.New("invalid Docker stdout frame")
		}
		reader.remaining = length
	}
	if uint32(len(target)) > reader.remaining {
		target = target[:reader.remaining]
	}
	count, err := reader.reader.Read(target)
	reader.remaining -= uint32(count)
	return count, err
}
