package docker

import (
	"context"
	"errors"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egressanchor"
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
	sidecar, created, err := ensureEgressSidecar(ctx, engine, request, policy)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
	if sidecar.state == runtimeport.ActualStopped {
		return runtimeport.EgressActual{}, errors.New("egress sidecar is stopped")
	}
	if sidecar.state == runtimeport.ActualCreated {
		if !created {
			// 崩溃恢复允许继续尚未启动且配置精确匹配的 sidecar；OpenStdin/StdinOnce
			// 保证仍只会有一个 attach 写端，任何 running/exited 漂移均走只读检查。
		}
		if err := writeEgressBootstrap(ctx, engine, r.netNSResolver, sidecar, request, policy); err != nil {
			return runtimeport.EgressActual{}, err
		}
	}
	attestation, err := waitEgressAttestation(ctx, engine, sidecar.id, request.ReadyTimeout)
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

// InspectEgress 只读验证 network、sidecar 安全快照、netns 与 attestation，不创建、
// 启动、exec 或修复任何资源。
func (r *Runtime) InspectEgress(ctx context.Context, request runtimeport.EgressRequest) (runtimeport.EgressActual, error) {
	engine, err := r.validateEgressRequest(request)
	if err != nil {
		return runtimeport.EgressActual{}, err
	}
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
	attestation, err := copyEgressAttestation(ctx, engine, sidecar.id)
	if err != nil {
		return runtimeport.EgressActual{}, errors.New("read egress attestation")
	}
	if err := r.validateEgressAttestation(attestation, sidecar, request, policy); err != nil {
		return runtimeport.EgressActual{}, err
	}
	actual.Attestation = attestation
	return actual, nil
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
		return nil, errors.New("Docker engine does not support egress resources")
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
