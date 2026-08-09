//go:build integration

package integration

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/runnerclient"
	"minisandbox/internal/runtime/docker"
	"minisandbox/pkg/protocol"
)

// TestEgressImmutableCIDRPolicy 使用宿主机 dummy 公网地址作为完全本地的允许夹具，
// 同时验证 bridge gateway、内部地址族、metadata 与额外 CIDR 均不能建立新连接。
func TestEgressImmutableCIDRPolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux is required for deterministic host network fixtures")
	}
	egressImage := os.Getenv(integrationEgressImageEnv)
	if egressImage == "" || !strings.Contains(egressImage, "@sha256:") {
		t.Skip("set MINISANDBOX_TEST_EGRESS_IMAGE to a locally available digest reference")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 is required for the local public-address fixture")
	}
	harness := newDockerHarness(t)
	info, err := harness.client.Info(t.Context(), mobyclient.InfoOptions{})
	if err == nil && strings.Contains(strings.ToLower(info.Info.OperatingSystem), "docker desktop") {
		t.Skip("native Linux Docker is required for host netns inode attestation")
	}
	publicAddress := "11.254.253.1"
	addHostAddress(t, publicAddress)
	publicListener := listenFixture(t, publicAddress)
	serveConnections(t, publicListener)

	image := integrationImage()
	harness.ensureImage(t, image)
	harness.ensureImage(t, egressImage)
	instance := harness.startSandboxdWithConfig(t, outboundConfig(egressImage, ""))
	sandbox := createSandboxWithNetwork(t, instance.baseURL, image, true)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	containerID := harness.runningContainerID(t, sandbox.ID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandbox.ID)

	assertTCPProbeExit(t, client, publicListener.Addr().String(), 0)
	network, err := harness.client.NetworkInspect(t.Context(), docker.EgressNetworkName, mobyclient.NetworkInspectOptions{})
	if err != nil || len(network.Network.IPAM.Config) == 0 || !network.Network.IPAM.Config[0].Gateway.IsValid() {
		t.Fatalf("inspect managed gateway: %v", err)
	}
	for _, address := range []string{
		net.JoinHostPort(network.Network.IPAM.Config[0].Gateway.String(), "1"),
		"10.0.0.1:1", "100.64.0.1:1", "169.254.169.254:80", "172.16.0.1:1", "192.168.0.1:1",
		"[fc00::1]:1", "[fe80::1]:1",
	} {
		assertTCPProbeExit(t, client, address, 20)
	}
	server, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{
		Argv: []string{executionHelperPath, "tcp-server", "0.0.0.0:39091"},
	})
	if err != nil {
		t.Fatalf("start inbound fixture: %v", err)
	}
	waitForTCPProbe(t, client, "127.0.0.1:39091")
	sidecar := inspectContainer(t, harness.client, "minisandbox-egress-"+sandbox.ID)
	if sidecar.NetworkSettings == nil || len(sidecar.NetworkSettings.Networks) == 0 {
		t.Fatal("sidecar network address is unavailable")
	}
	var sidecarAddress string
	for _, endpoint := range sidecar.NetworkSettings.Networks {
		if endpoint != nil && endpoint.IPAddress.IsValid() {
			sidecarAddress = endpoint.IPAddress.String()
			break
		}
	}
	if sidecarAddress == "" {
		t.Fatal("sidecar IPv4 address is unavailable")
	}
	connection, dialErr := net.DialTimeout("tcp", net.JoinHostPort(sidecarAddress, "39091"), 750*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		t.Fatal("unsolicited inbound connection crossed the INPUT boundary")
	}
	_, _ = client.Cancel(t.Context(), server.ExecutionID)

	denied := harness.startSandboxdWithConfig(t, outboundConfig(egressImage, publicAddress+"/32"))
	deniedSandbox := createSandboxWithNetwork(t, denied.baseURL, image, true)
	harness.trackSandbox(deniedSandbox.ID)
	waitSandboxState(t, denied.baseURL, deniedSandbox.ID, protocol.SandboxStateRunning)
	deniedContainerID := harness.runningContainerID(t, deniedSandbox.ID)
	installExecutionHelper(t, harness.client, deniedContainerID, buildExecutionHelper(t))
	assertTCPProbeExit(t, denied.runnerClient(t, deniedSandbox.ID), publicListener.Addr().String(), 20)
}

func outboundConfig(egressImage, deniedCIDR string) func(string) string {
	return func(content string) string {
		content = strings.Replace(content, "allow_outbound: false", "allow_outbound: true", 1)
		egress := "egress:\n  image: " + strconv.Quote(egressImage) + "\n  ready_timeout: \"15s\"\n"
		if deniedCIDR != "" {
			egress += "  egress_denied_cidrs: [" + strconv.Quote(deniedCIDR) + "]\n"
		}
		return strings.Replace(content, "runner:\n", egress+"runner:\n", 1)
	}
}

func addHostAddress(t *testing.T, address string) {
	t.Helper()
	command := exec.Command("ip", "address", "add", address+"/32", "dev", "lo")
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("cannot install local public-address fixture: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "address", "del", address+"/32", "dev", "lo").Run()
	})
}

func listenFixture(t *testing.T, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatalf("listen local network fixture: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func serveConnections(t *testing.T, listener net.Listener) {
	t.Helper()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
}

func assertTCPProbeExit(t *testing.T, client *runnerclient.Client, address string, want int) {
	t.Helper()
	events := executeForeground(t, client, protocol.ExecuteRequest{
		Argv: []string{executionHelperPath, "tcp-probe", address, "750"},
	})
	terminal := events[len(events)-1]
	if terminal.Type != protocol.EventExited || terminal.ExitCode == nil || *terminal.ExitCode != want {
		t.Fatalf("tcp probe %s: %+v", address, terminal)
	}
}

func waitForTCPProbe(t *testing.T, client *runnerclient.Client, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := pollCondition(ctx, 50*time.Millisecond, func() (bool, error) {
		events := executeForeground(t, client, protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "tcp-probe", address, "250"},
		})
		terminal := events[len(events)-1]
		return terminal.Type == protocol.EventExited && terminal.ExitCode != nil && *terminal.ExitCode == 0, nil
	}); err != nil {
		t.Fatalf("wait for TCP fixture: %v", err)
	}
}
