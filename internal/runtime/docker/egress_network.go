package docker

import (
	"context"
	"errors"

	cerrdefs "github.com/containerd/errdefs"
	mobynetwork "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egresspolicy"
)

const (
	// EgressNetworkName 是所有 outbound sidecar 使用的服务级 user-defined bridge。
	EgressNetworkName = "minisandbox-egress"
	// LabelResourceRole 区分主容器、egress sidecar 与服务级 bridge。
	LabelResourceRole = "minisandbox.io/resource-role"
	// LabelEgressPolicyHash 保存规范化策略身份，不保存完整 CIDR。
	LabelEgressPolicyHash = "minisandbox.io/egress-policy-hash"
	// LabelEgressImage 保存 sidecar 精确 artifact digest。
	LabelEgressImage = "minisandbox.io/egress-image"
	// LabelEgressProtocol 保存 sidecar bootstrap 精确协议版本。
	LabelEgressProtocol = "minisandbox.io/egress-protocol-version"

	resourceRoleEgressNetwork = "egress-network"
	resourceRoleEgressSidecar = "egress-sidecar"
	resourceRoleMain          = "main"
)

var egressNetworkLabels = map[string]string{
	LabelManaged:       labelManagedValue,
	LabelSchemaVersion: labelSchemaVersionValue,
	LabelResourceRole:  resourceRoleEgressNetwork,
}

type egressNetwork struct {
	id       string
	networks []egresspolicy.ManagedNetwork
}

func ensureEgressNetwork(ctx context.Context, engine EgressEngine) (egressNetwork, error) {
	inspection, err := engine.NetworkInspect(ctx, EgressNetworkName, mobyclient.NetworkInspectOptions{})
	if err == nil {
		return validateEgressNetwork(inspection.Network)
	}
	if !cerrdefs.IsNotFound(err) {
		return egressNetwork{}, runtimeUnavailable(err)
	}
	enabled := true
	created, err := engine.NetworkCreate(ctx, EgressNetworkName, mobyclient.NetworkCreateOptions{
		Driver: "bridge", Scope: "local", EnableIPv4: &enabled, EnableIPv6: &enabled,
		IPAM: &mobynetwork.IPAM{Driver: "default"}, Labels: cloneStrings(egressNetworkLabels),
	})
	if err != nil && !cerrdefs.IsConflict(err) {
		return egressNetwork{}, runtimeUnavailable(err)
	}
	inspection, err = engine.NetworkInspect(ctx, EgressNetworkName, mobyclient.NetworkInspectOptions{})
	if err != nil {
		return egressNetwork{}, runtimeUnavailable(err)
	}
	result, err := validateEgressNetwork(inspection.Network)
	if err != nil {
		return egressNetwork{}, err
	}
	if created.ID != "" && created.ID != result.id {
		return egressNetwork{}, containerIdentityConflict()
	}
	return result, nil
}

func validateEgressNetwork(network mobynetwork.Inspect) (egressNetwork, error) {
	if network.ID == "" || network.Name != EgressNetworkName || network.Driver != "bridge" || network.Scope != "local" ||
		network.Internal || network.Ingress || network.ConfigOnly || !network.EnableIPv4 || !network.EnableIPv6 {
		return egressNetwork{}, containerIdentityConflict()
	}
	for key, expected := range egressNetworkLabels {
		if network.Labels[key] != expected {
			return egressNetwork{}, containerIdentityConflict()
		}
	}
	if len(network.IPAM.Config) == 0 {
		return egressNetwork{}, errors.New("managed egress network IPAM is missing")
	}
	facts := make([]egresspolicy.ManagedNetwork, 0, len(network.IPAM.Config))
	for _, config := range network.IPAM.Config {
		if !config.Subnet.IsValid() || !config.Gateway.IsValid() {
			return egressNetwork{}, errors.New("managed egress network IPAM is invalid")
		}
		facts = append(facts, egresspolicy.ManagedNetwork{
			Subnets: []string{config.Subnet.String()}, Gateways: []string{config.Gateway.String()},
		})
	}
	return egressNetwork{id: network.ID, networks: facts}, nil
}

func cloneStrings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
