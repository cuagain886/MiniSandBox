package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
	runtimeport "minisandbox/internal/runtime"
)

const (
	egressSidecarNamePrefix = "minisandbox-egress-"
	egressEntrypoint        = "/usr/local/bin/egressd"
	egressWorkingDirectory  = "/"
	egressPathEnvironment   = "PATH=/usr/sbin:/usr/bin:/sbin:/bin"
)

type egressSidecar struct {
	id    string
	state runtimeport.ActualState
	pid   int
}

func egressSidecarName(sandboxID string) string { return egressSidecarNamePrefix + sandboxID }

func sidecarLabels(request runtimeport.EgressRequest, policy egresspolicy.Policy) map[string]string {
	return map[string]string{
		LabelManaged: labelManagedValue, LabelSchemaVersion: labelSchemaVersionValue,
		LabelSandboxID: request.SandboxID, LabelResourceRole: resourceRoleEgressSidecar,
		LabelEgressPolicyHash: policy.Hash, LabelEgressImage: request.Image,
		LabelEgressProtocol: intString(egresspolicy.CurrentProtocolVersion),
	}
}

func buildEgressSidecarOptions(request runtimeport.EgressRequest, policy egresspolicy.Policy) (mobyclient.ContainerCreateOptions, error) {
	if !validSandboxID(request.SandboxID) || !egressnft.ValidImageDigest(request.Image) || request.AnchorUID == 0 || request.AnchorGID == 0 {
		return mobyclient.ContainerCreateOptions{}, errors.New("egress sidecar request is invalid")
	}
	resources, err := convertResourceLimits(request.Limits)
	if err != nil {
		return mobyclient.ContainerCreateOptions{}, err
	}
	// Docker 把 MemorySwap=0 解释为“由守护进程选择默认值”，通常会得到内存上限的
	// 两倍。显式令总 memory+swap 上限等于 memory，才能跨守护进程稳定地禁用额外 swap。
	resources.MemorySwap = resources.Memory
	return mobyclient.ContainerCreateOptions{
		Name: egressSidecarName(request.SandboxID),
		Config: &mobycontainer.Config{
			User: "0:0", Image: request.Image,
			Entrypoint:  []string{egressEntrypoint, "bootstrap"},
			Env:         []string{egressPathEnvironment},
			WorkingDir:  egressWorkingDirectory,
			AttachStdin: true, AttachStdout: true, OpenStdin: true, StdinOnce: false,
			NetworkDisabled: false, Labels: sidecarLabels(request, policy),
		},
		HostConfig: &mobycontainer.HostConfig{
			NetworkMode: mobycontainer.NetworkMode(EgressNetworkName), Privileged: false,
			CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN", "SETUID", "SETGID"},
			SecurityOpt: []string{noNewPrivilegesSecurity}, ReadonlyRootfs: true,
			RestartPolicy: mobycontainer.RestartPolicy{Name: mobycontainer.RestartPolicyDisabled},
			LogConfig:     mobycontainer.LogConfig{Type: "none", Config: map[string]string{}}, Resources: resources,
		},
		NetworkingConfig: &mobynetwork.NetworkingConfig{EndpointsConfig: map[string]*mobynetwork.EndpointSettings{
			EgressNetworkName: {},
		}},
		Platform: &ocispec.Platform{OS: "linux", Architecture: "amd64"},
	}, nil
}

func ensureEgressSidecar(ctx context.Context, engine EgressEngine, request runtimeport.EgressRequest, policy egresspolicy.Policy) (egressSidecar, bool, error) {
	name := egressSidecarName(request.SandboxID)
	inspection, err := engine.ContainerInspect(ctx, name, mobyclient.ContainerInspectOptions{})
	if err == nil {
		sidecar, err := validateEgressSidecar(inspection.Container, request, policy)
		return sidecar, false, err
	}
	if !cerrdefs.IsNotFound(err) {
		return egressSidecar{}, false, runtimeUnavailable(err)
	}
	options, err := buildEgressSidecarOptions(request, policy)
	if err != nil {
		return egressSidecar{}, false, err
	}
	created, err := engine.ContainerCreate(ctx, options)
	if err != nil {
		if cerrdefs.IsConflict(err) {
			return egressSidecar{}, false, containerIdentityConflict()
		}
		return egressSidecar{}, false, &ContainerCreateFailedError{cause: err}
	}
	if created.ID == "" {
		return egressSidecar{}, true, &ContainerCreateFailedError{cause: errors.New("docker returned empty egress sidecar ID")}
	}
	return egressSidecar{id: created.ID, state: runtimeport.ActualCreated}, true, nil
}

func validateEgressSidecar(container mobycontainer.InspectResponse, request runtimeport.EgressRequest, policy egresspolicy.Policy) (egressSidecar, error) {
	if container.ID == "" || strings.TrimPrefix(container.Name, "/") != egressSidecarName(request.SandboxID) ||
		container.Config == nil || container.HostConfig == nil || container.State == nil {
		return egressSidecar{}, containerIdentityConflict()
	}
	expected, err := buildEgressSidecarOptions(request, policy)
	if err != nil {
		return egressSidecar{}, err
	}
	config, host := container.Config, container.HostConfig
	if config.Image != request.Image || config.User != expected.Config.User || config.WorkingDir != expected.Config.WorkingDir ||
		!reflect.DeepEqual(config.Entrypoint, expected.Config.Entrypoint) || !reflect.DeepEqual(config.Env, expected.Config.Env) ||
		len(config.Cmd) != 0 || len(config.ExposedPorts) != 0 || len(config.Volumes) != 0 ||
		!config.AttachStdin || !config.AttachStdout || config.AttachStderr || !config.OpenStdin || config.StdinOnce || config.Tty || config.NetworkDisabled ||
		!managedLabelsMatch(config.Labels, expected.Config.Labels) || host.NetworkMode != expected.HostConfig.NetworkMode ||
		host.Privileged || !sameCapabilitySet(host.CapDrop, expected.HostConfig.CapDrop) || !sameCapabilitySet(host.CapAdd, expected.HostConfig.CapAdd) ||
		!reflect.DeepEqual(host.SecurityOpt, expected.HostConfig.SecurityOpt) || !host.ReadonlyRootfs ||
		host.RestartPolicy.Name != mobycontainer.RestartPolicyDisabled || host.RestartPolicy.MaximumRetryCount != 0 ||
		host.LogConfig.Type != "none" || len(host.LogConfig.Config) != 0 || len(host.Tmpfs) != 0 ||
		!sameEgressResources(host.Resources, expected.HostConfig.Resources) || len(host.Binds) != 0 || len(host.Mounts) != 0 ||
		len(host.PortBindings) != 0 || len(host.Devices) != 0 || len(host.DeviceRequests) != 0 || len(host.VolumesFrom) != 0 {
		return egressSidecar{}, containerIdentityConflict()
	}
	if container.NetworkSettings == nil || len(container.NetworkSettings.Networks) != 1 || container.NetworkSettings.Networks[EgressNetworkName] == nil {
		return egressSidecar{}, containerIdentityConflict()
	}
	state := runtimeport.ActualStopped
	switch container.State.Status {
	case mobycontainer.StateCreated:
		state = runtimeport.ActualCreated
	case mobycontainer.StateRunning:
		state = runtimeport.ActualRunning
	case mobycontainer.StateExited:
		state = runtimeport.ActualStopped
	default:
		return egressSidecar{}, containerIdentityConflict()
	}
	return egressSidecar{id: container.ID, state: state, pid: container.State.Pid}, nil
}

// sameCapabilitySet 接受 Docker inspect 对 capability 添加 CAP_ 前缀或调整顺序的
// 等价表示，但拒绝缺失、额外和重复 capability，避免表示规范化放宽权限边界。
func sameCapabilitySet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, capability := range expected {
		normalized := strings.TrimPrefix(strings.ToUpper(capability), "CAP_")
		if normalized == "" {
			return false
		}
		wanted[normalized] = struct{}{}
	}
	if len(wanted) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(actual))
	for _, capability := range actual {
		normalized := strings.TrimPrefix(strings.ToUpper(capability), "CAP_")
		if _, ok := wanted[normalized]; !ok {
			return false
		}
		if _, duplicate := seen[normalized]; duplicate {
			return false
		}
		seen[normalized] = struct{}{}
	}
	return len(seen) == len(wanted)
}

// sameEgressResources 只比较 MiniSandbox 主动控制的稳定字段。Docker inspect 会为
// Resources 的其他零值字段填充 nil/空切片等等价表示，不能用结构体全等判断漂移。
func sameEgressResources(actual, expected mobycontainer.Resources) bool {
	if actual.NanoCPUs != expected.NanoCPUs || actual.Memory != expected.Memory || actual.MemorySwap != expected.MemorySwap ||
		actual.PidsLimit == nil || expected.PidsLimit == nil {
		return false
	}
	return *actual.PidsLimit == *expected.PidsLimit
}

func managedLabelsMatch(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func bootstrapEgressSidecar(ctx context.Context, engine EgressEngine, resolver NetNSResolver, sidecar egressSidecar, request runtimeport.EgressRequest, policy egresspolicy.Policy) (egressanchor.Attestation, error) {
	session, err := openEgressControlSession(ctx, engine, sidecar.id, request.ReadyTimeout)
	if err != nil {
		return egressanchor.Attestation{}, err
	}
	defer session.close()
	if _, err := engine.ContainerStart(ctx, sidecar.id, mobyclient.ContainerStartOptions{}); err != nil && !cerrdefs.IsNotModified(err) {
		return egressanchor.Attestation{}, errors.New("start egress sidecar")
	}
	inspection, err := engine.ContainerInspect(ctx, sidecar.id, mobyclient.ContainerInspectOptions{})
	if err != nil || inspection.Container.State == nil || inspection.Container.State.Status != mobycontainer.StateRunning {
		return egressanchor.Attestation{}, errors.New("inspect started egress sidecar")
	}
	identity, err := resolver.Identity(inspection.Container.State.Pid)
	if err != nil {
		return egressanchor.Attestation{}, errors.New("resolve egress network namespace")
	}
	bootstrap := egressnft.Bootstrap{
		Policy: policy, NetworkNamespace: identity, ImageDigest: request.Image,
		AnchorUID: request.AnchorUID, AnchorGID: request.AnchorGID,
	}
	controlRequest, err := newEgressControlRequest(egresscontrol.RequestBootstrap, &bootstrap)
	if err != nil {
		return egressanchor.Attestation{}, err
	}
	return session.exchange(controlRequest)
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func inspectEgressAttestation(ctx context.Context, engine EgressEngine, containerID string, timeout time.Duration) (egressanchor.Attestation, error) {
	session, err := openEgressControlSession(ctx, engine, containerID, timeout)
	if err != nil {
		return egressanchor.Attestation{}, err
	}
	defer session.close()
	request, err := newEgressControlRequest(egresscontrol.RequestInspect, nil)
	if err != nil {
		return egressanchor.Attestation{}, err
	}
	return session.exchange(request)
}

func intString(value int) string { return fmt.Sprintf("%d", value) }
