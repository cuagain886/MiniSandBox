//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
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

	if sidecar.Config.Image != egressImage || sidecar.Config.User != "65532:65532" || sidecar.Config.Labels[dockerruntime.LabelSandboxID] != outbound.ID ||
		sidecar.Config.Labels[dockerruntime.LabelResourceRole] != "egress-sidecar" || sidecar.Config.Labels[dockerruntime.LabelEgressImage] != egressImage {
		t.Fatal("sidecar immutable identity or labels drifted")
	}
	if sidecar.HostConfig.Privileged || !sidecar.HostConfig.ReadonlyRootfs || sidecar.HostConfig.RestartPolicy.Name != mobycontainer.RestartPolicyDisabled ||
		len(sidecar.Config.Env) != 0 || len(sidecar.Config.Cmd) != 0 || len(sidecar.Config.ExposedPorts) != 0 || len(sidecar.HostConfig.PortBindings) != 0 ||
		len(sidecar.HostConfig.Binds) != 0 || len(sidecar.HostConfig.Mounts) != 0 || len(sidecar.HostConfig.Devices) != 0 || len(sidecar.HostConfig.VolumesFrom) != 0 {
		t.Fatal("sidecar least-privilege profile drifted")
	}
	assertCapabilitySet(t, "sidecar CapDrop", sidecar.HostConfig.CapDrop, []string{"ALL"})
	assertCapabilitySet(t, "sidecar CapAdd", sidecar.HostConfig.CapAdd, []string{"NET_ADMIN"})
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

	attestationRaw := copyRegularFile(t, harness.client, sidecar.ID, egressanchor.DefaultAttestationPath)
	attestation, err := egressanchor.ParseAttestation(attestationRaw)
	if err != nil {
		t.Fatalf("parse sidecar attestation: %v", err)
	}
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
