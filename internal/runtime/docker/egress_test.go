package docker

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyimage "github.com/moby/moby/api/types/image"
	mobynetwork "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
	"minisandbox/internal/egresspolicy"
	runtimeport "minisandbox/internal/runtime"
)

// TestEnsureEgressNetworkCreateAndReuse 验证首次创建、并发冲突后回读和精确复用均
// 依赖同名受管 bridge 的 labels/driver/IPAM，而不会接管外部网络。
func TestEnsureEgressNetworkCreateAndReuse(t *testing.T) {
	inspection := sampleEgressNetwork()
	inspectCalls := 0
	createCalls := 0
	engine := &fakeEngine{
		networkInspectFunc: func(context.Context, string, mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
			inspectCalls++
			if inspectCalls == 1 {
				return mobyclient.NetworkInspectResult{}, cerrdefs.ErrNotFound
			}
			return mobyclient.NetworkInspectResult{Network: inspection}, nil
		},
		networkCreateFunc: func(_ context.Context, name string, options mobyclient.NetworkCreateOptions) (mobyclient.NetworkCreateResult, error) {
			createCalls++
			if name != EgressNetworkName || options.Driver != "bridge" || options.EnableIPv4 == nil || !*options.EnableIPv4 ||
				options.EnableIPv6 == nil || !*options.EnableIPv6 || !reflect.DeepEqual(options.Labels, egressNetworkLabels) {
				t.Fatalf("unsafe network create options: %+v", options)
			}
			return mobyclient.NetworkCreateResult{ID: inspection.ID}, nil
		},
	}
	created, err := ensureEgressNetwork(context.Background(), engine)
	if err != nil {
		t.Fatalf("ensure network: %v", err)
	}
	if created.id != inspection.ID || len(created.networks) != 2 || createCalls != 1 {
		t.Fatalf("unexpected managed network: %+v createCalls=%d", created, createCalls)
	}

	engine.networkInspectFunc = func(context.Context, string, mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
		return mobyclient.NetworkInspectResult{Network: inspection}, nil
	}
	if _, err := ensureEgressNetwork(context.Background(), engine); err != nil || createCalls != 1 {
		t.Fatalf("matching network was not reused: err=%v creates=%d", err, createCalls)
	}
}

// TestValidateEgressNetworkDrift 验证同名外部网络、driver/schema/IPv6/IPAM 漂移均
// fail closed。
func TestValidateEgressNetworkDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*mobynetwork.Inspect)
	}{
		{name: "external labels", mutate: func(n *mobynetwork.Inspect) { n.Labels = map[string]string{} }},
		{name: "driver drift", mutate: func(n *mobynetwork.Inspect) { n.Driver = "overlay" }},
		{name: "schema drift", mutate: func(n *mobynetwork.Inspect) { n.Labels[LabelSchemaVersion] = "2" }},
		{name: "internal network", mutate: func(n *mobynetwork.Inspect) { n.Internal = true }},
		{name: "IPv6 disabled", mutate: func(n *mobynetwork.Inspect) { n.EnableIPv6 = false }},
		{name: "IPAM missing", mutate: func(n *mobynetwork.Inspect) { n.IPAM.Config = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			network := sampleEgressNetwork()
			test.mutate(&network)
			if _, err := validateEgressNetwork(network); err == nil {
				t.Fatal("drifted network accepted")
			}
		})
	}
}

// TestBuildEgressSidecarSecuritySnapshot 锁定 sidecar 的镜像、身份、stdin、网络、
// capabilities、只读 rootfs、关闭日志、可重连 stdin、restart policy、资源和零暴露面。
func TestBuildEgressSidecarSecuritySnapshot(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	options, err := buildEgressSidecarOptions(request, policy)
	if err != nil {
		t.Fatalf("build sidecar options: %v", err)
	}
	if options.Name != egressSidecarName(request.SandboxID) || options.Config.Image != request.Image ||
		options.Config.User != "0:0" || strings.Join(options.Config.Entrypoint, " ") != egressEntrypoint+" bootstrap" ||
		!options.Config.AttachStdin || !options.Config.AttachStdout || options.Config.AttachStderr ||
		!options.Config.OpenStdin || options.Config.StdinOnce || options.Config.Tty || len(options.Config.Env) != 0 || len(options.Config.Cmd) != 0 {
		t.Fatalf("unsafe sidecar config: %+v", options.Config)
	}
	host := options.HostConfig
	if host.NetworkMode != mobycontainer.NetworkMode(EgressNetworkName) || host.Privileged ||
		!reflect.DeepEqual(host.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(host.CapAdd, []string{"NET_ADMIN", "SETUID", "SETGID"}) ||
		!host.ReadonlyRootfs || host.RestartPolicy.Name != mobycontainer.RestartPolicyDisabled ||
		host.LogConfig.Type != "none" || len(host.Tmpfs) != 0 || len(host.Binds) != 0 || len(host.Mounts) != 0 ||
		len(host.PortBindings) != 0 || len(host.Devices) != 0 || len(host.DeviceRequests) != 0 {
		t.Fatalf("unsafe sidecar host config: %+v", host)
	}
	if len(options.NetworkingConfig.EndpointsConfig) != 1 || options.NetworkingConfig.EndpointsConfig[EgressNetworkName] == nil {
		t.Fatalf("sidecar is not attached only to managed network: %+v", options.NetworkingConfig)
	}
}

// TestValidateEgressControlChannelDrift 验证一次性 stdin、缺失 stdout、持久日志或
// 任意 tmpfs 回退都不能被当成可重连内存 attestation sidecar 复用。
func TestValidateEgressControlChannelDrift(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	tests := []struct {
		name   string
		mutate func(*mobycontainer.InspectResponse)
	}{
		{name: "stdin once", mutate: func(container *mobycontainer.InspectResponse) { container.Config.StdinOnce = true }},
		{name: "stdout detached", mutate: func(container *mobycontainer.InspectResponse) { container.Config.AttachStdout = false }},
		{name: "stderr attached", mutate: func(container *mobycontainer.InspectResponse) { container.Config.AttachStderr = true }},
		{name: "persistent log driver", mutate: func(container *mobycontainer.InspectResponse) { container.HostConfig.LogConfig.Type = "json-file" }},
		{name: "attestation tmpfs", mutate: func(container *mobycontainer.InspectResponse) {
			container.HostConfig.Tmpfs = map[string]string{"/run/minisandbox-egress": "rw,size=64k"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			container := sampleEgressSidecar(t, request, policy, mobycontainer.StateRunning)
			test.mutate(&container)
			if _, err := validateEgressSidecar(container, request, policy); err == nil {
				t.Fatal("drifted attach control channel accepted")
			}
		})
	}
}

// TestBuildMainContainerNetworkModes 验证默认/false 保持 network none，true 只能
// 共享已验证 sidecar netns，且主容器不获得 endpoint、端口或网络 capability。
func TestBuildMainContainerNetworkModes(t *testing.T) {
	sandbox := testDockerSandbox()
	names, err := NamesForSandbox(t.TempDir(), sandbox.ID)
	if err != nil {
		t.Fatalf("derive names: %v", err)
	}
	withoutNetwork, err := buildContainerCreateOptions(sandbox, names)
	if err != nil {
		t.Fatalf("build network-none options: %v", err)
	}
	if !withoutNetwork.Config.NetworkDisabled || withoutNetwork.HostConfig.NetworkMode != "none" {
		t.Fatalf("default network mode drifted: %+v", withoutNetwork.HostConfig)
	}

	request, policy := sampleEgressRequest(t)
	sandbox.Spec.Network.Outbound = true
	ready := &runtimeport.EgressActual{
		SandboxID: sandbox.ID, ContainerID: "sidecar-id", NetworkID: "network-id",
		State: runtimeport.ActualRunning, Policy: policy,
		Attestation: egressanchor.Attestation{
			ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
			PolicyHash: policy.Hash, NetworkNamespace: "linux-netns:4:4026533000", ImageDigest: request.Image,
		},
	}
	outbound, err := buildContainerCreateOptionsWithEgress(sandbox, names, ready)
	if err != nil {
		t.Fatalf("build outbound options: %v", err)
	}
	if outbound.Config.NetworkDisabled || outbound.HostConfig.NetworkMode != "container:sidecar-id" || outbound.NetworkingConfig != nil ||
		len(outbound.HostConfig.PortBindings) != 0 {
		t.Fatalf("outbound topology drifted: options=%+v", outbound)
	}
	for _, capability := range outbound.HostConfig.CapAdd {
		if capability == "NET_ADMIN" || capability == "NET_RAW" {
			t.Fatalf("main sandbox gained forbidden capability: %v", outbound.HostConfig.CapAdd)
		}
	}

	for _, invalid := range []*runtimeport.EgressActual{nil, {}, {State: runtimeport.ActualRunning}, {State: runtimeport.ActualRunning, ContainerID: "sidecar-id", NetworkID: "network-id"}} {
		if _, err := buildContainerCreateOptionsWithEgress(sandbox, names, invalid); err == nil {
			t.Fatalf("invalid Ready evidence accepted: %+v", invalid)
		}
	}
}

// TestRuntimeEgressOptionsFailClosed 验证 outbound 平台配置必须完整、digest 固定且
// deny CIDR 合法；apply 时复制切片，避免调用方后续修改运行中策略。
func TestRuntimeEgressOptionsFailClosed(t *testing.T) {
	request, _ := sampleEgressRequest(t)
	valid := &EgressPlatformConfig{
		Image: request.Image, AdditionalDeniedCIDRs: request.AdditionalDeniedCIDRs,
		AnchorUID: request.AnchorUID, AnchorGID: request.AnchorGID,
		Limits: request.Limits, ReadyTimeout: request.ReadyTimeout,
	}
	base := RuntimeOptions{DataDirectory: t.TempDir(), Artifacts: testArtifactProvider(), CreateTimeout: time.Second, Egress: valid}
	if err := validateRuntimeOptions(base); err != nil {
		t.Fatalf("valid egress options rejected: %v", err)
	}
	runtime := &Runtime{}
	runtime.applyOptions(base)
	valid.AdditionalDeniedCIDRs[0] = "9.9.9.0/24"
	if runtime.egressConfig.AdditionalDeniedCIDRs[0] != "8.8.8.0/24" {
		t.Fatal("runtime retained mutable egress deny slice")
	}

	invalid := *runtime.egressConfig
	invalid.Image = "egressd:latest"
	base.Egress = &invalid
	if err := validateRuntimeOptions(base); err == nil {
		t.Fatal("tag-only egress artifact accepted")
	}
	invalid = *runtime.egressConfig
	invalid.AdditionalDeniedCIDRs = []string{"invalid-cidr"}
	base.Egress = &invalid
	if err := validateRuntimeOptions(base); err == nil {
		t.Fatal("invalid egress deny CIDR accepted")
	}
}

// TestEnsureEgressSidecarIdempotent 验证精确匹配的 created sidecar 被复用，外部同名
// 容器或安全配置漂移不会触发覆盖创建。
func TestEnsureEgressSidecarIdempotent(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	inspection := sampleEgressSidecar(t, request, policy, mobycontainer.StateCreated)
	createCalls := 0
	engine := &fakeEngine{
		containerInspectFunc: func(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{Container: inspection}, nil
		},
		containerCreateFunc: func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
			createCalls++
			return mobyclient.ContainerCreateResult{}, nil
		},
	}
	sidecar, created, err := ensureEgressSidecar(context.Background(), engine, request, policy)
	if err != nil || created || sidecar.id != inspection.ID || createCalls != 0 {
		t.Fatalf("matching sidecar not reused: sidecar=%+v created=%v err=%v", sidecar, created, err)
	}
	inspection.Config.Labels[LabelResourceRole] = "external"
	if _, _, err := ensureEgressSidecar(context.Background(), engine, request, policy); err == nil || createCalls != 0 {
		t.Fatal("drifted sidecar was accepted or overwritten")
	}
}

// TestBootstrapEgressSidecar 验证 attach 发生在 start 前，bootstrap 响应完成后仅关闭
// 当前连接而不关闭容器 stdin，且所有安全身份来自可信 runtime 输入。
func TestBootstrapEgressSidecar(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	attestation := egressanchor.Attestation{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, NetworkNamespace: "linux-netns:4:4026533000", ImageDigest: request.Image,
		CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	connection, output := newResponsiveControlConn(attestation)
	order := make([]string, 0, 3)
	engine := &fakeEngine{
		containerAttachFunc: func(_ context.Context, id string, options mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
			order = append(order, "attach")
			if id != "sidecar-id" || !options.Stream || !options.Stdin || !options.Stdout || options.Stderr || options.Logs {
				t.Fatalf("unsafe attach options: %+v", options)
			}
			return mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{Conn: connection, Reader: output}}, nil
		},
		containerStartFunc: func(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
			order = append(order, "start")
			return mobyclient.ContainerStartResult{}, nil
		},
		containerInspectFunc: func(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			order = append(order, "inspect")
			return mobyclient.ContainerInspectResult{Container: mobycontainer.InspectResponse{State: &mobycontainer.State{Status: mobycontainer.StateRunning, Pid: 4242}}}, nil
		},
	}
	resolver := fakeNetNSResolver{identity: "linux-netns:4:4026533000"}
	got, err := bootstrapEgressSidecar(context.Background(), engine, resolver, egressSidecar{id: "sidecar-id"}, request, policy)
	if err != nil || got != attestation {
		t.Fatalf("bootstrap sidecar: got=%+v err=%v", got, err)
	}
	if strings.Join(order, " ") != "attach start inspect" || connection.writeClosed || !connection.closed {
		t.Fatalf("bootstrap order/close mismatch: order=%v conn=%+v", order, connection)
	}
	if connection.request.Type != egresscontrol.RequestBootstrap || connection.request.Bootstrap == nil ||
		connection.request.Bootstrap.Policy.Hash != policy.Hash || connection.request.Bootstrap.NetworkNamespace != resolver.identity ||
		connection.request.Bootstrap.ImageDigest != request.Image {
		t.Fatalf("bootstrap identity mismatch: %+v", connection.request)
	}
}

// TestInspectEgressAttestation 验证恢复与 execution 准入使用新的随机关联字段发起
// 只读 inspect，且请求中不包含 bootstrap 或策略更新数据。
func TestInspectEgressAttestation(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	attestation := egressanchor.Attestation{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, NetworkNamespace: "linux-netns:4:4026533000", ImageDigest: request.Image,
		CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	connection, output := newResponsiveControlConn(attestation)
	engine := &fakeEngine{containerAttachFunc: func(context.Context, string, mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
		return mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{Conn: connection, Reader: output}}, nil
	}}
	got, err := inspectEgressAttestation(context.Background(), engine, "sidecar-id", time.Second)
	if err != nil || got != attestation {
		t.Fatalf("inspect attestation: got=%+v err=%v", got, err)
	}
	if connection.request.Type != egresscontrol.RequestInspect || connection.request.Bootstrap != nil ||
		connection.request.RequestID == "" || connection.request.Nonce == "" {
		t.Fatalf("invalid inspect request: %+v", connection.request)
	}
}

// TestRuntimeEnsureEgressEndToEnd 验证 Runtime 按 image→network→sidecar create→
// attach→start→bootstrap→attestation 顺序收敛，并在第二次调用时纯复用 Ready sidecar。
func TestRuntimeEnsureEgressEndToEnd(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	running := sampleEgressSidecar(t, request, policy, mobycontainer.StateRunning)
	attestation := egressanchor.Attestation{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, NetworkNamespace: "linux-netns:4:4026533000", ImageDigest: request.Image,
		CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	connections := make([]*responsiveControlConn, 0, 2)
	inspectCalls, createCalls, attachCalls, startCalls := 0, 0, 0, 0
	engine := &fakeEngine{
		imageInspectFunc: func(context.Context, string, ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
			return mobyclient.ImageInspectResult{InspectResponse: mobyimage.InspectResponse{Os: "linux", Architecture: "amd64"}}, nil
		},
		networkInspectFunc: func(context.Context, string, mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
			return mobyclient.NetworkInspectResult{Network: sampleEgressNetwork()}, nil
		},
		containerInspectFunc: func(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			inspectCalls++
			switch inspectCalls {
			case 1:
				return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
			case 2:
				return mobyclient.ContainerInspectResult{Container: mobycontainer.InspectResponse{State: &mobycontainer.State{Status: mobycontainer.StateRunning, Pid: 4242}}}, nil
			default:
				return mobyclient.ContainerInspectResult{Container: running}, nil
			}
		},
		containerCreateFunc: func(context.Context, mobyclient.ContainerCreateOptions) (mobyclient.ContainerCreateResult, error) {
			createCalls++
			return mobyclient.ContainerCreateResult{ID: running.ID}, nil
		},
		containerAttachFunc: func(context.Context, string, mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
			attachCalls++
			connection, output := newResponsiveControlConn(attestation)
			connections = append(connections, connection)
			return mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{Conn: connection, Reader: output}}, nil
		},
		containerStartFunc: func(context.Context, string, mobyclient.ContainerStartOptions) (mobyclient.ContainerStartResult, error) {
			startCalls++
			return mobyclient.ContainerStartResult{}, nil
		},
	}
	runtime := &Runtime{engine: engine, netNSResolver: fakeNetNSResolver{identity: attestation.NetworkNamespace}, createTimeout: time.Second}
	actual, err := runtime.EnsureEgress(context.Background(), request)
	if err != nil {
		t.Fatalf("ensure egress: %v", err)
	}
	if actual.State != runtimeport.ActualRunning || actual.ContainerID != running.ID || actual.NetworkID != "network-id" || actual.Policy.Hash != policy.Hash {
		t.Fatalf("unexpected egress actual: %+v", actual)
	}
	if createCalls != 1 || attachCalls != 1 || startCalls != 1 || connections[0].writeClosed ||
		connections[0].request.Type != egresscontrol.RequestBootstrap {
		t.Fatalf("unexpected bootstrap calls: create=%d attach=%d start=%d conn=%+v", createCalls, attachCalls, startCalls, connections[0])
	}

	actual, err = runtime.EnsureEgress(context.Background(), request)
	if err != nil || actual.State != runtimeport.ActualRunning {
		t.Fatalf("reuse egress: actual=%+v err=%v", actual, err)
	}
	if createCalls != 1 || attachCalls != 2 || startCalls != 1 || connections[1].request.Type != egresscontrol.RequestInspect {
		t.Fatalf("Ready sidecar was mutated: create=%d attach=%d start=%d", createCalls, attachCalls, startCalls)
	}
}

// TestRuntimeCheckEgressForExecution 验证每次新 execution 前会只读比较 sidecar、
// attestation、Docker container mode 与 runner netns，任一身份漂移即关闭准入。
func TestRuntimeCheckEgressForExecution(t *testing.T) {
	request, policy := sampleEgressRequest(t)
	running := sampleEgressSidecar(t, request, policy, mobycontainer.StateRunning)
	identity := "linux-netns:4:4026533000"
	attestation := egressanchor.Attestation{
		ProtocolVersion: policy.ProtocolVersion, RuleSchemaVersion: policy.RuleSchemaVersion,
		PolicyHash: policy.Hash, NetworkNamespace: identity, ImageDigest: request.Image,
		CreatedAt: time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC),
	}
	mainMode := mobycontainer.NetworkMode("container:" + running.ID)
	engine := &fakeEngine{
		networkInspectFunc: func(context.Context, string, mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
			return mobyclient.NetworkInspectResult{Network: sampleEgressNetwork()}, nil
		},
		containerInspectFunc: func(_ context.Context, name string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			if name == egressSidecarName(request.SandboxID) || name == running.ID {
				return mobyclient.ContainerInspectResult{Container: running}, nil
			}
			return mobyclient.ContainerInspectResult{Container: mobycontainer.InspectResponse{HostConfig: &mobycontainer.HostConfig{NetworkMode: mainMode}}}, nil
		},
		containerAttachFunc: func(context.Context, string, mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
			connection, output := newResponsiveControlConn(attestation)
			return mobyclient.ContainerAttachResult{HijackedResponse: mobyclient.HijackedResponse{Conn: connection, Reader: output}}, nil
		},
	}
	runtime := &Runtime{engine: engine, netNSResolver: fakeNetNSResolver{identity: identity}, createTimeout: time.Second}
	if err := runtime.CheckEgressForExecution(context.Background(), request, identity); err != nil {
		t.Fatalf("healthy egress rejected: %v", err)
	}
	if err := runtime.CheckEgressForExecution(context.Background(), request, "linux-netns:4:999"); err == nil {
		t.Fatal("runner netns drift accepted")
	}
	mainMode = "none"
	if err := runtime.CheckEgressForExecution(context.Background(), request, identity); err == nil {
		t.Fatal("main Docker network mode drift accepted")
	}
}

func sampleEgressRequest(t *testing.T) (runtimeport.EgressRequest, egresspolicy.Policy) {
	t.Helper()
	request := runtimeport.EgressRequest{
		SandboxID:             "123e4567-e89b-42d3-a456-426614174000",
		Image:                 "registry.example/minisandbox/egressd@sha256:" + strings.Repeat("a", 64),
		AdditionalDeniedCIDRs: []string{"8.8.8.0/24"}, AnchorUID: 65532, AnchorGID: 65532,
		Limits: domain.ResourceLimits{CPUQuotaMillis: 100, MemoryMiB: 64, PIDs: 16}, ReadyTimeout: time.Second,
	}
	policy, err := egresspolicy.Build(request.AdditionalDeniedCIDRs, []egresspolicy.ManagedNetwork{{
		Subnets: []string{"10.240.0.0/24"}, Gateways: []string{"10.240.0.1"},
	}})
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	return request, policy
}

func sampleEgressNetwork() mobynetwork.Inspect {
	return mobynetwork.Inspect{Network: mobynetwork.Network{
		ID: "network-id", Name: EgressNetworkName, Driver: "bridge", Scope: "local",
		EnableIPv4: true, EnableIPv6: true, Labels: cloneStrings(egressNetworkLabels),
		IPAM: mobynetwork.IPAM{Driver: "default", Config: []mobynetwork.IPAMConfig{
			{Subnet: netip.MustParsePrefix("10.240.0.0/24"), Gateway: netip.MustParseAddr("10.240.0.1")},
			{Subnet: netip.MustParsePrefix("fd42:240::/64"), Gateway: netip.MustParseAddr("fd42:240::1")},
		}},
	}}
}

func sampleEgressSidecar(t *testing.T, request runtimeport.EgressRequest, policy egresspolicy.Policy, state mobycontainer.ContainerState) mobycontainer.InspectResponse {
	t.Helper()
	options, err := buildEgressSidecarOptions(request, policy)
	if err != nil {
		t.Fatalf("build sidecar options: %v", err)
	}
	return mobycontainer.InspectResponse{
		ID: "sidecar-id", Name: "/" + options.Name, Config: options.Config, HostConfig: options.HostConfig,
		State:           &mobycontainer.State{Status: state, Pid: 4242},
		NetworkSettings: &mobycontainer.NetworkSettings{Networks: map[string]*mobynetwork.EndpointSettings{EgressNetworkName: {}}},
	}
}

type responsiveControlConn struct {
	recordingConn
	attestation egressanchor.Attestation
	response    *io.PipeWriter
	request     egresscontrol.Request
}

func newResponsiveControlConn(attestation egressanchor.Attestation) (*responsiveControlConn, *bufio.Reader) {
	reader, writer := io.Pipe()
	return &responsiveControlConn{attestation: attestation, response: writer}, bufio.NewReader(reader)
}

func (connection *responsiveControlConn) Write(data []byte) (int, error) {
	written, err := connection.recordingConn.Write(data)
	if err != nil {
		return written, err
	}
	request, err := egresscontrol.ReadRequest(bytes.NewReader(connection.data.Bytes()))
	if err != nil {
		return 0, err
	}
	connection.request = request
	response, err := egresscontrol.EncodeResponse(egresscontrol.Response{
		RequestID: request.RequestID, Nonce: request.Nonce, Attestation: connection.attestation,
	})
	if err != nil {
		return 0, err
	}
	go func() {
		_, _ = connection.response.Write(dockerStdoutFrame(1, response))
	}()
	return written, nil
}

func (connection *responsiveControlConn) Close() error {
	_ = connection.response.Close()
	return connection.recordingConn.Close()
}

type fakeNetNSResolver struct {
	identity string
	err      error
}

func (resolver fakeNetNSResolver) Identity(int) (string, error) {
	return resolver.identity, resolver.err
}

type recordingConn struct {
	data        bytes.Buffer
	writeClosed bool
	closed      bool
}

func (connection *recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (connection *recordingConn) Write(data []byte) (int, error)   { return connection.data.Write(data) }
func (connection *recordingConn) Close() error                     { connection.closed = true; return nil }
func (connection *recordingConn) CloseWrite() error                { connection.writeClosed = true; return nil }
func (connection *recordingConn) LocalAddr() net.Addr              { return fakeAddress("local") }
func (connection *recordingConn) RemoteAddr() net.Addr             { return fakeAddress("remote") }
func (connection *recordingConn) SetDeadline(time.Time) error      { return nil }
func (connection *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *recordingConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddress string

func (address fakeAddress) Network() string { return string(address) }
func (address fakeAddress) String() string  { return string(address) }
