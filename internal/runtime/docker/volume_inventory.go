package docker

import (
	"context"
	"sort"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

// InventoryManagedVolumes 枚举并逐项复核 MiniSandbox workspace 卷，不挂载卷也不读取卷内内容。
func (r *Runtime) InventoryManagedVolumes(ctx context.Context) ([]runtimeport.ManagedVolumeObservation, error) {
	filters := make(mobyclient.Filters).Add("label", LabelManaged+"="+labelManagedValue)
	listed, err := r.engine.VolumeList(ctx, mobyclient.VolumeListOptions{Filters: filters})
	if err != nil {
		return nil, runtimeUnavailable(err)
	}

	result := make([]runtimeport.ManagedVolumeObservation, 0, len(listed.Items))
	for _, summary := range listed.Items {
		// daemon 可能忽略或错误实现 filter；外部卷必须在 inspect 前就被排除。
		if summary.Labels[LabelManaged] != labelManagedValue {
			continue
		}
		inspection, err := r.engine.VolumeInspect(ctx, summary.Name, mobyclient.VolumeInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			result = append(result, runtimeport.ManagedVolumeObservation{
				VolumeName: summary.Name, DiscoveryIssue: runtimeport.DiscoveryInspectUnavailable,
			})
			continue
		}
		result = append(result, mapManagedVolumeInspection(inspection.Volume))
	}

	markDuplicateVolumes(result)
	sort.Slice(result, func(left, right int) bool {
		if result[left].SandboxID != result[right].SandboxID {
			return result[left].SandboxID < result[right].SandboxID
		}
		return result[left].VolumeName < result[right].VolumeName
	})
	return result, nil
}

func mapManagedVolumeInspection(volume mobyvolume.Volume) runtimeport.ManagedVolumeObservation {
	observation := runtimeport.ManagedVolumeObservation{VolumeName: volume.Name}
	if validSandboxID(volume.Labels[LabelSandboxID]) {
		observation.SandboxID = volume.Labels[LabelSandboxID]
	}
	schema, err := strconv.Atoi(volume.Labels[LabelSchemaVersion])
	if err != nil || schema != 1 && schema != 2 {
		observation.DiscoveryIssue = runtimeport.DiscoverySchemaUnsupported
		return observation
	}
	observation.SchemaVersion = schema
	metadata, err := ParseLabels(volume.Labels)
	if err != nil {
		observation.DiscoveryIssue = runtimeport.DiscoveryLabelsInvalid
		return observation
	}
	observation.SandboxID, observation.SpecHash = metadata.SandboxID, metadata.SpecHash
	// resource-role 是 v2 后期新增的兼容扩展；缺失表示旧 workspace，其他职责则拒绝接管。
	if role := volume.Labels[LabelResourceRole]; role != "" && role != resourceRoleWorkspace {
		observation.DiscoveryIssue = runtimeport.DiscoveryRoleUnsupported
		return observation
	}
	if volume.Name != workspaceName(metadata.SandboxID) || metadata.Workspace != volume.Name {
		observation.DiscoveryIssue = runtimeport.DiscoveryProfileInvalid
	}
	return observation
}

func markDuplicateVolumes(observations []runtimeport.ManagedVolumeObservation) {
	counts := make(map[string]int, len(observations))
	for _, observation := range observations {
		if observation.SandboxID != "" {
			counts[observation.SandboxID]++
		}
	}
	for index := range observations {
		if counts[observations[index].SandboxID] > 1 {
			// 重复身份比单项 profile 损坏更重要，聚合器不能从多个候选中猜测所有权。
			observations[index].DiscoveryIssue = runtimeport.DiscoveryDuplicateResource
		}
	}
}
