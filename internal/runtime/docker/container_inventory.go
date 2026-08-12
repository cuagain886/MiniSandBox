package docker

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	"minisandbox/internal/egressnft"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
)

// InventoryManagedContainers 枚举并逐个 inspect 全部 running/stopped 受管容器。
// 单项损坏返回安全 anomaly observation；list 后已消失的容器直接忽略。
func (r *Runtime) InventoryManagedContainers(ctx context.Context) (observations []runtimeport.ManagedContainerObservation, resultErr error) {
	defer func() { r.observeDocker("inventory", resultErr) }()
	filters := make(mobyclient.Filters).Add("label", LabelManaged+"="+labelManagedValue)
	listed, err := r.engine.ContainerList(ctx, mobyclient.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, runtimeUnavailable(err)
	}
	result := make([]runtimeport.ManagedContainerObservation, 0, len(listed.Items))
	for _, summary := range listed.Items {
		inspection, err := r.engine.ContainerInspect(ctx, summary.ID, mobyclient.ContainerInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			result = append(result, runtimeport.ManagedContainerObservation{ContainerID: summary.ID, DiscoveryIssue: runtimeport.DiscoveryInspectUnavailable})
			continue
		}
		result = append(result, mapManagedContainerInspection(inspection.Container))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].SandboxID != result[right].SandboxID {
			return result[left].SandboxID < result[right].SandboxID
		}
		if result[left].Role != result[right].Role {
			return result[left].Role < result[right].Role
		}
		return result[left].ContainerID < result[right].ContainerID
	})
	return result, nil
}

func mapManagedContainerInspection(container mobycontainer.InspectResponse) runtimeport.ManagedContainerObservation {
	observation := runtimeport.ManagedContainerObservation{ContainerID: container.ID}
	if createdAt, err := time.Parse(time.RFC3339Nano, container.Created); err == nil {
		observation.CreatedAt = createdAt.UTC()
	}
	if container.Config == nil || container.HostConfig == nil || container.State == nil {
		observation.DiscoveryIssue = runtimeport.DiscoveryProfileInvalid
		return observation
	}
	labels := container.Config.Labels
	if validSandboxID(labels[LabelSandboxID]) {
		observation.SandboxID = labels[LabelSandboxID]
	}
	schema, err := strconv.Atoi(labels[LabelSchemaVersion])
	if err != nil || schema != 1 && schema != 2 {
		observation.DiscoveryIssue = runtimeport.DiscoverySchemaUnsupported
		return observation
	}
	observation.SchemaVersion = schema
	state, err := mapSummaryState(container.State.Status)
	if err != nil {
		observation.DiscoveryIssue = runtimeport.DiscoveryStateUnsupported
		return observation
	}
	observation.State = state
	role := labels[LabelResourceRole]
	if role == resourceRoleEgressSidecar {
		return mapEgressContainerObservation(container, observation)
	}
	metadata, err := ParseLabels(labels)
	if err != nil {
		observation.DiscoveryIssue = runtimeport.DiscoveryLabelsInvalid
		return observation
	}
	observation.SandboxID, observation.Role = metadata.SandboxID, runtimeport.ContainerRoleMain
	if metadata.ExpiresAt != nil {
		expiresAt := metadata.ExpiresAt.UTC()
		observation.CreationExpiresAt = &expiresAt
	}
	observation.Name = strings.TrimPrefix(container.Name, "/")
	if observation.Name != containerName(metadata.SandboxID) {
		observation.DiscoveryIssue = runtimeport.DiscoveryProfileInvalid
		return observation
	}
	observation.SpecHash, observation.RunnerProtocolVersion = metadata.SpecHash, metadata.RunnerProtocolVersion
	mapSafeContainerProfile(container, &observation)
	return observation
}

func mapEgressContainerObservation(container mobycontainer.InspectResponse, observation runtimeport.ManagedContainerObservation) runtimeport.ManagedContainerObservation {
	labels := container.Config.Labels
	protocolVersion, err := strconv.Atoi(labels[LabelEgressProtocol])
	if !validSandboxID(labels[LabelSandboxID]) || err != nil || protocolVersion < 1 ||
		!validLowerHex(labels[LabelEgressPolicyHash], 64) || !egressnft.ValidImageDigest(labels[LabelEgressImage]) {
		observation.DiscoveryIssue = runtimeport.DiscoveryLabelsInvalid
		return observation
	}
	observation.SandboxID, observation.Role = labels[LabelSandboxID], runtimeport.ContainerRoleEgress
	observation.Name = strings.TrimPrefix(container.Name, "/")
	if observation.Name != egressSidecarName(observation.SandboxID) {
		observation.DiscoveryIssue = runtimeport.DiscoveryProfileInvalid
		return observation
	}
	observation.EgressProtocolVersion, observation.EgressPolicyHash = protocolVersion, labels[LabelEgressPolicyHash]
	mapSafeContainerProfile(container, &observation)
	return observation
}

func mapSafeContainerProfile(container mobycontainer.InspectResponse, observation *runtimeport.ManagedContainerObservation) {
	host := container.HostConfig
	config := container.Config
	observation.ImageReference = config.Image
	if container.Platform == "linux" {
		observation.PlatformOS, observation.PlatformArch = "linux", "amd64"
	}
	mode := string(host.NetworkMode)
	switch {
	case mode == "none":
		observation.NetworkMode = "none"
	case mode == EgressNetworkName:
		observation.NetworkMode = "managed-egress"
	case strings.HasPrefix(mode, "container:"):
		observation.NetworkMode = "container"
		observation.NetworkPeerContainerID = strings.TrimPrefix(mode, "container:")
	default:
		observation.NetworkMode = "other"
	}
	observation.RestartPolicy = string(host.RestartPolicy.Name)
	observation.Privileged, observation.ReadonlyRootfs = host.Privileged, host.ReadonlyRootfs
	observation.CapAdd = append([]string(nil), host.CapAdd...)
	observation.CapDrop = append([]string(nil), host.CapDrop...)
	for _, option := range host.SecurityOpt {
		if option == noNewPrivilegesSecurity {
			observation.NoNewPrivileges = true
		}
	}
	observation.NamespaceProfileValid = host.PidMode == "" && host.IpcMode == "" && host.UTSMode == ""
	observation.PortProfileValid = len(config.ExposedPorts) == 0 && len(host.PortBindings) == 0
	observation.DeviceProfileValid = len(host.Devices) == 0 && len(host.DeviceRequests) == 0 && len(host.VolumesFrom) == 0
	observation.ResourceProfileValid = mapObservedResources(host.Resources, observation)
	observation.ProcessProfileValid = processProfileMatches(config, observation.Role)
	observation.MountProfileValid = mountProfileMatches(container, observation.Role)
	for _, mount := range container.Mounts {
		if mount.Type == "volume" && mount.Destination == domain.WorkspaceMountPath {
			observation.Workspace, observation.WorkspaceDestination = mount.Name, mount.Destination
		}
	}
}

func mapObservedResources(resources mobycontainer.Resources, observation *runtimeport.ManagedContainerObservation) bool {
	if resources.NanoCPUs <= 0 || resources.NanoCPUs%nanoCPUsPerMilliCPU != 0 ||
		resources.Memory <= 0 || resources.Memory%bytesPerMiB != 0 || resources.PidsLimit == nil || *resources.PidsLimit <= 0 {
		return false
	}
	observation.CPUQuotaMillis = resources.NanoCPUs / nanoCPUsPerMilliCPU
	observation.MemoryMiB = resources.Memory / bytesPerMiB
	observation.PIDs = *resources.PidsLimit
	return true
}

func processProfileMatches(config *mobycontainer.Config, role runtimeport.ContainerResourceRole) bool {
	if role == runtimeport.ContainerRoleMain {
		return config.User == "0:0" && config.WorkingDir == domain.WorkspaceMountPath &&
			sameStringSlice(config.Entrypoint, fixedEntrypoint) && len(config.Cmd) == 0
	}
	return config.User == "0:0" && config.WorkingDir == egressWorkingDirectory &&
		sameStringSlice(config.Entrypoint, []string{egressEntrypoint, "bootstrap"}) && len(config.Cmd) == 0
}

func mountProfileMatches(container mobycontainer.InspectResponse, role runtimeport.ContainerResourceRole) bool {
	if role == runtimeport.ContainerRoleEgress {
		return len(container.Mounts) == 0 && len(container.HostConfig.Binds) == 0 && len(container.HostConfig.Mounts) == 0
	}
	workspace, runtimeDirectory := 0, 0
	for _, mount := range container.Mounts {
		switch {
		case mount.Type == "volume" && mount.Destination == domain.WorkspaceMountPath:
			workspace++
		case mount.Type == "bind" && mount.Destination == runnerbootstrap.RuntimeDirectory:
			runtimeDirectory++
		default:
			return false
		}
	}
	return workspace == 1 && runtimeDirectory == 1 && len(container.Mounts) == 2
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
