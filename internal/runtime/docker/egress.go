package docker

import (
	"context"
	"errors"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/egresspolicy"
	runtimeport "minisandbox/internal/runtime"
)

// EnsureEgress 幂等创建或复用服务级 bridge 与当前 sandbox 的 namespace anchor，
// 通过 attach stdin 写入唯一 bootstrap 帧，并只在 attestation 全部匹配后返回 Ready。
func (r *Runtime) EnsureEgress(ctx context.Context, request runtimeport.EgressRequest) (runtimeport.EgressActual, error) {
	engine, err := r.validateEgressRequest(request)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	unlock := r.lockEgressAttach(request.SandboxID)
	defer unlock()
	if _, err := ensureImage(ctx, r.engine, request.Image, r.createTimeout); err != nil {
		return runtimeport.EgressActual{}, err
	}
	network, err := ensureEgressNetwork(ctx, engine)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	policy, err := egresspolicy.Build(request.AdditionalDeniedCIDRs, network.networks)
	if err != nil {
		return runtimeport.EgressActual{}, errors.New("build managed egress policy")
	}
	sidecar, _, err := ensureEgressSidecar(ctx, engine, request, policy)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	if sidecar.state == runtimeport.ActualStopped {
		return runtimeport.EgressActual{}, errors.New("egress sidecar is stopped")
	}
	var attestation egressanchor.Attestation
	if sidecar.state == runtimeport.ActualCreated {
		// 崩溃恢复只继续尚未启动且配置精确匹配的 sidecar。启动后由 bootstrap
		// 响应证明降权完成；running sidecar 只能接受只读 inspect。
		attestation, err = bootstrapEgressSidecar(ctx, engine, r.netNSResolver, sidecar, request, policy)
	} else {
		attestation, err = inspectEgressAttestation(ctx, engine, sidecar.id, request.ReadyTimeout)
	}
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	inspection, err := engine.ContainerInspect(ctx, sidecar.id, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return runtimeport.EgressActual{}, runtimeUnavailable(err)
	}
	verified, err := validateEgressSidecar(inspection.Container, request, policy)
	if err != nil || verified.state != runtimeport.ActualRunning {
		return runtimeport.EgressActual{}, errors.New("egress sidecar is not running")
	}
	if err := r.validateEgressAttestation(attestation, verified, request, policy); err != nil {
		return runtimeport.EgressActual{}, err
	}
	return runtimeport.EgressActual{
		SandboxID: request.SandboxID, ContainerID: verified.id, NetworkID: network.id,
		State: runtimeport.ActualRunning, Policy: policy, Attestation: attestation,
	}, nil
}

// CheckEgressForExecution 是不做修复的 runtime health port；只有 running sidecar、
// 精确 Docker container:<id> 拓扑、attestation 与 runner netns 三方一致时才开放准入。
func (r *Runtime) CheckEgressForExecution(ctx context.Context, request runtimeport.EgressRequest, runnerNetNS string) error {
	if !egressnft.ValidNetworkNamespace(runnerNetNS) {
		return errors.New("runner network namespace identity is invalid")
	}
	actual, err := r.InspectEgress(ctx, request)
	if err != nil || actual.State != runtimeport.ActualRunning || actual.Attestation.NetworkNamespace != runnerNetNS {
		return errors.New("egress sidecar is unhealthy")
	}
	inspection, err := r.engine.ContainerInspect(ctx, containerName(request.SandboxID), mobyclient.ContainerInspectOptions{})
	if err != nil {
		return errors.New("inspect outbound sandbox network mode")
	}
	if inspection.Container.HostConfig == nil || inspection.Container.HostConfig.NetworkMode != mobycontainer.NetworkMode("container:"+actual.ContainerID) {
		return errors.New("outbound sandbox network mode drifted")
	}
	return nil
}

// CheckSandboxEgress 从 runtime 受信配置重建请求并执行 outbound 双重就绪校验。
func (r *Runtime) CheckSandboxEgress(ctx context.Context, sandboxID, runnerNetNS string) error {
	request, err := r.egressRequest(sandboxID)
	if err != nil {
		return err
	}
	return r.CheckEgressForExecution(ctx, request, runnerNetNS)
}

func (r *Runtime) validateMainContainerNetwork(ctx context.Context, containerID string, outbound bool, egress *runtimeport.EgressActual) error {
	inspection, err := r.engine.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil || inspection.Container.Config == nil || inspection.Container.HostConfig == nil {
		return errors.New("inspect sandbox network mode")
	}
	config, host := inspection.Container.Config, inspection.Container.HostConfig
	if outbound {
		if !validReadyEgress(egress) || config.NetworkDisabled || host.NetworkMode != mobycontainer.NetworkMode("container:"+egress.ContainerID) {
			return errors.New("outbound sandbox network mode drifted")
		}
	} else if !config.NetworkDisabled || host.NetworkMode != mobycontainer.NetworkMode("none") {
		return errors.New("network-none sandbox mode drifted")
	}
	for _, capability := range host.CapAdd {
		normalized := strings.TrimPrefix(strings.ToUpper(capability), "CAP_")
		if normalized == "NET_ADMIN" || normalized == "NET_RAW" {
			return errors.New("sandbox retains forbidden network capability")
		}
	}
	if len(host.PortBindings) != 0 || len(host.Links) != 0 {
		return errors.New("sandbox has forbidden network exposure")
	}
	return nil
}

// InspectEgress 只读验证 network、sidecar 安全快照、netns 与 attestation，不创建、
// 启动、exec 或修复任何资源。
func (r *Runtime) InspectEgress(ctx context.Context, request runtimeport.EgressRequest) (runtimeport.EgressActual, error) {
	engine, err := r.validateEgressRequest(request)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	unlock := r.lockEgressAttach(request.SandboxID)
	defer unlock()
	networkInspection, err := engine.NetworkInspect(ctx, EgressNetworkName, mobyclient.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return runtimeport.EgressActual{SandboxID: request.SandboxID, State: runtimeport.ActualMissing}, nil
		}
		return runtimeport.EgressActual{}, runtimeUnavailable(err)
	}
	network, err := validateEgressNetwork(networkInspection.Network)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	policy, err := egresspolicy.Build(request.AdditionalDeniedCIDRs, network.networks)
	if err != nil {
		return runtimeport.EgressActual{}, errors.New("build managed egress policy")
	}
	inspection, err := engine.ContainerInspect(ctx, egressSidecarName(request.SandboxID), mobyclient.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return runtimeport.EgressActual{SandboxID: request.SandboxID, NetworkID: network.id, State: runtimeport.ActualMissing, Policy: policy}, nil
		}
		return runtimeport.EgressActual{}, runtimeUnavailable(err)
	}
	sidecar, err := validateEgressSidecar(inspection.Container, request, policy)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	actual := runtimeport.EgressActual{
		SandboxID: request.SandboxID, ContainerID: sidecar.id, NetworkID: network.id,
		State: sidecar.state, Policy: policy,
	}
	if sidecar.state != runtimeport.ActualRunning {
		return actual, nil
	}
	attestation, err := inspectEgressAttestation(ctx, engine, sidecar.id, request.ReadyTimeout)
	if err != nil {
		return runtimeport.EgressActual{}, errors.New("query egress attestation")
	}
	if err := r.validateEgressAttestation(attestation, sidecar, request, policy); err != nil {
		return runtimeport.EgressActual{}, err
	}
	actual.Attestation = attestation
	return actual, nil
}

func newEgressControlRequest(kind egresscontrol.RequestType, bootstrap *egressnft.Bootstrap) (egresscontrol.Request, error) {
	requestID, nonce, err := egresscontrol.NewCorrelation()
	if err != nil {
		return egresscontrol.Request{}, err
	}
	return egresscontrol.Request{Type: kind, RequestID: requestID, Nonce: nonce, Bootstrap: bootstrap}, nil
}

func (r *Runtime) validateEgressRequest(request runtimeport.EgressRequest) (EgressEngine, error) {
	if r == nil || r.engine == nil || r.netNSResolver == nil || !validSandboxID(request.SandboxID) ||
		request.ReadyTimeout <= 0 || request.ReadyTimeout > 2*time.Minute || !egressnft.ValidImageDigest(request.Image) ||
		request.AnchorUID == 0 || request.AnchorGID == 0 {
		return nil, errors.New("egress runtime request is invalid")
	}
	if _, err := convertResourceLimits(request.Limits); err != nil {
		return nil, err
	}
	engine, ok := r.engine.(EgressEngine)
	if !ok {
		return nil, errors.New("docker engine does not support egress resources")
	}
	return engine, nil
}

func (r *Runtime) validateEgressAttestation(attestation egressanchor.Attestation, sidecar egressSidecar, request runtimeport.EgressRequest, policy egresspolicy.Policy) error {
	if attestation.ProtocolVersion != policy.ProtocolVersion || attestation.RuleSchemaVersion != policy.RuleSchemaVersion ||
		attestation.PolicyHash != policy.Hash || attestation.ImageDigest != request.Image {
		return errors.New("egress attestation identity mismatch")
	}
	identity, err := r.netNSResolver.Identity(sidecar.pid)
	if err != nil || identity != attestation.NetworkNamespace {
		return errors.New("egress attestation network namespace mismatch")
	}
	return nil
}

var _ runtimeport.EgressRuntime = (*Runtime)(nil)
var _ runtimeport.ExecutionEgressGate = (*Runtime)(nil)
