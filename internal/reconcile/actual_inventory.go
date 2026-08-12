package reconcile

import (
	"sort"
	"strings"

	"minisandbox/internal/egresspolicy"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
)

// ActualAnomalyCode 是聚合阶段可确定、无需 Store 参与的资源矛盾类型。
type ActualAnomalyCode string

const (
	// ActualAnomalyResourceDamaged 表示单项 inventory 已拒绝信任该资源。
	ActualAnomalyResourceDamaged ActualAnomalyCode = "RESOURCE_DAMAGED"
	// ActualAnomalyDuplicateMain 表示同一 ID 有多个主容器。
	ActualAnomalyDuplicateMain ActualAnomalyCode = "DUPLICATE_MAIN"
	// ActualAnomalyDuplicateEgress 表示同一 ID 有多个 egress sidecar。
	ActualAnomalyDuplicateEgress ActualAnomalyCode = "DUPLICATE_EGRESS"
	// ActualAnomalyDuplicateWorkspace 表示同一 ID 有多个 workspace 卷。
	ActualAnomalyDuplicateWorkspace ActualAnomalyCode = "DUPLICATE_WORKSPACE"
	// ActualAnomalyDuplicateDirectory 表示同一 ID 有多个 runtime directory observation。
	ActualAnomalyDuplicateDirectory ActualAnomalyCode = "DUPLICATE_DIRECTORY"
	// ActualAnomalyOrphanEgress 表示 sidecar 没有对应主容器。
	ActualAnomalyOrphanEgress ActualAnomalyCode = "ORPHAN_EGRESS"
	// ActualAnomalyEgressMissing 表示主容器声明 outbound，但 sidecar 缺失。
	ActualAnomalyEgressMissing ActualAnomalyCode = "EGRESS_MISSING"
	// ActualAnomalyEgressUnexpected 表示主容器声明无网络，但发现 sidecar。
	ActualAnomalyEgressUnexpected ActualAnomalyCode = "EGRESS_UNEXPECTED"
	// ActualAnomalyNetworkProfile 表示主容器网络模式不属于公开 outbound 语义。
	ActualAnomalyNetworkProfile ActualAnomalyCode = "NETWORK_PROFILE_INVALID"
	// ActualAnomalyNetNSConflict 表示主容器加入的 network namespace 不是该 sidecar。
	ActualAnomalyNetNSConflict ActualAnomalyCode = "NETNS_CONFLICT"
	// ActualAnomalySpecHashConflict 表示 bundle 内的安全规格摘要不一致。
	ActualAnomalySpecHashConflict ActualAnomalyCode = "SPEC_HASH_CONFLICT"
	// ActualAnomalySchemaConflict 表示 Docker 资源的恢复 label schema 不一致。
	ActualAnomalySchemaConflict ActualAnomalyCode = "SCHEMA_CONFLICT"
	// ActualAnomalyIdentityConflict 表示归组 ID 与资源内部投影的 ID 相互矛盾。
	ActualAnomalyIdentityConflict ActualAnomalyCode = "IDENTITY_CONFLICT"
	// ActualAnomalyProtocolConflict 表示 runner 或 egress 协议不是当前精确版本。
	ActualAnomalyProtocolConflict ActualAnomalyCode = "PROTOCOL_CONFLICT"
	// ActualAnomalyPolicyConflict 表示 sidecar policy 摘要不满足安全格式。
	ActualAnomalyPolicyConflict ActualAnomalyCode = "POLICY_CONFLICT"
)

// ActualAnomaly 是不包含 raw labels、宿主机路径或资源内容的稳定诊断。
type ActualAnomaly struct {
	// Code 是机器可读的矛盾分类。
	Code ActualAnomalyCode
	// Resource 是 main、egress、workspace、directory 之一；跨资源矛盾时为空。
	Resource string
	// Detail 是下层 inventory 的稳定 issue code；不得放原始错误文本。
	Detail string
}

// ActualResourceSnapshot 汇总单个 sandbox 的不可变实际资源视图。
type ActualResourceSnapshot struct {
	// SandboxID 是全部已归组事实共有的规范 ID。
	SandboxID string
	// Main 是唯一主容器；重复时为 nil 并记录 anomaly。
	Main *runtimeport.ManagedContainerObservation
	// Egress 是唯一 egress sidecar；重复时为 nil 并记录 anomaly。
	Egress *runtimeport.ManagedContainerObservation
	// Workspace 是唯一 workspace 卷；重复时为 nil 并记录 anomaly。
	Workspace *runtimeport.ManagedVolumeObservation
	// Directory 是唯一 runtime directory；重复时为 nil 并记录 anomaly。
	Directory *runtimeport.RuntimeDirectoryObservation
	// Anomalies 是按 code/resource/detail 稳定排序的安全诊断副本。
	Anomalies []ActualAnomaly
}

// ActualResourceInventory 是按 ID 排序的 bundle 快照及无法安全归组的异常。
type ActualResourceInventory struct {
	// Sandboxes 是规范 sandbox ID 对应的稳定快照。
	Sandboxes []ActualResourceSnapshot
	// UnscopedAnomalies 保存损坏到无法取得规范 ID 的事实。
	UnscopedAnomalies []ActualAnomaly
}

type actualResourceGroup struct {
	main      []runtimeport.ManagedContainerObservation
	egress    []runtimeport.ManagedContainerObservation
	workspace []runtimeport.ManagedVolumeObservation
	directory []runtimeport.RuntimeDirectoryObservation
	anomalies []ActualAnomaly
}

// AggregateActualResources 仅分类容器、卷和目录事实，不读取 Store、不作恢复决策且不修改资源。
func AggregateActualResources(
	containers []runtimeport.ManagedContainerObservation,
	volumes []runtimeport.ManagedVolumeObservation,
	directories []runtimeport.RuntimeDirectoryObservation,
) ActualResourceInventory {
	groups := make(map[string]*actualResourceGroup)
	result := ActualResourceInventory{}
	groupFor := func(id string) *actualResourceGroup {
		group := groups[id]
		if group == nil {
			group = &actualResourceGroup{}
			groups[id] = group
		}
		return group
	}
	for _, source := range containers {
		observation := cloneContainerObservation(source)
		if observation.SandboxID == "" {
			result.UnscopedAnomalies = appendDamaged(result.UnscopedAnomalies, string(observation.Role), observation.DiscoveryIssue)
			continue
		}
		group := groupFor(observation.SandboxID)
		if observation.DiscoveryIssue != "" {
			group.anomalies = appendDamaged(group.anomalies, string(observation.Role), observation.DiscoveryIssue)
		}
		switch observation.Role {
		case runtimeport.ContainerRoleMain:
			group.main = append(group.main, observation)
		case runtimeport.ContainerRoleEgress:
			group.egress = append(group.egress, observation)
		default:
			group.anomalies = appendDamaged(group.anomalies, "container", runtimeport.DiscoveryRoleUnsupported)
		}
	}
	for _, source := range volumes {
		observation := source
		if observation.SandboxID == "" {
			result.UnscopedAnomalies = appendDamaged(result.UnscopedAnomalies, "workspace", observation.DiscoveryIssue)
			continue
		}
		group := groupFor(observation.SandboxID)
		if observation.DiscoveryIssue != "" {
			group.anomalies = appendDamaged(group.anomalies, "workspace", observation.DiscoveryIssue)
		}
		group.workspace = append(group.workspace, observation)
	}
	for _, source := range directories {
		observation := cloneDirectoryObservation(source)
		if observation.SandboxID == "" {
			result.UnscopedAnomalies = appendDamaged(result.UnscopedAnomalies, "directory", observation.DiscoveryIssue)
			continue
		}
		group := groupFor(observation.SandboxID)
		if observation.DiscoveryIssue != "" {
			group.anomalies = appendDamaged(group.anomalies, "directory", observation.DiscoveryIssue)
		}
		group.directory = append(group.directory, observation)
	}

	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result.Sandboxes = make([]ActualResourceSnapshot, 0, len(ids))
	for _, id := range ids {
		result.Sandboxes = append(result.Sandboxes, buildActualSnapshot(id, groups[id]))
	}
	sortAnomalies(result.UnscopedAnomalies)
	return result
}

func buildActualSnapshot(id string, group *actualResourceGroup) ActualResourceSnapshot {
	snapshot := ActualResourceSnapshot{SandboxID: id, Anomalies: append([]ActualAnomaly(nil), group.anomalies...)}
	if len(group.main) == 1 {
		copy := cloneContainerObservation(group.main[0])
		snapshot.Main = &copy
	} else if len(group.main) > 1 {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyDuplicateMain, Resource: "main"})
	}
	if len(group.egress) == 1 {
		copy := cloneContainerObservation(group.egress[0])
		snapshot.Egress = &copy
	} else if len(group.egress) > 1 {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyDuplicateEgress, Resource: "egress"})
	}
	if len(group.workspace) == 1 {
		copy := group.workspace[0]
		snapshot.Workspace = &copy
	} else if len(group.workspace) > 1 {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyDuplicateWorkspace, Resource: "workspace"})
	}
	if len(group.directory) == 1 {
		copy := cloneDirectoryObservation(group.directory[0])
		snapshot.Directory = &copy
	} else if len(group.directory) > 1 {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyDuplicateDirectory, Resource: "directory"})
	}
	validateActualBundle(&snapshot)
	sortAnomalies(snapshot.Anomalies)
	return snapshot
}

func validateActualBundle(snapshot *ActualResourceSnapshot) {
	if snapshot.Main == nil {
		if snapshot.Egress != nil {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyOrphanEgress, Resource: "egress"})
		}
		return
	}
	if snapshot.Main.RunnerProtocolVersion != runnerbootstrap.CurrentProtocolVersion {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyProtocolConflict, Resource: "main"})
	}
	if snapshot.Egress != nil {
		if snapshot.Egress.EgressProtocolVersion != egresspolicy.CurrentProtocolVersion {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyProtocolConflict, Resource: "egress"})
		}
		if len(snapshot.Egress.EgressPolicyHash) != 64 || strings.Trim(snapshot.Egress.EgressPolicyHash, "0123456789abcdef") != "" {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyPolicyConflict, Resource: "egress"})
		}
	}
	switch snapshot.Main.NetworkMode {
	case "none":
		if snapshot.Egress != nil {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyEgressUnexpected})
		}
	case "container":
		if snapshot.Egress == nil {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyEgressMissing})
		} else if snapshot.Main.NetworkPeerContainerID != snapshot.Egress.ContainerID {
			snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyNetNSConflict})
		}
	default:
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyNetworkProfile, Resource: "main"})
	}
	if snapshot.Workspace != nil && snapshot.Main.SpecHash != snapshot.Workspace.SpecHash {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalySpecHashConflict})
	}
	if snapshot.Directory != nil && snapshot.Directory.Manifest != nil && snapshot.Main.SpecHash != snapshot.Directory.Manifest.SpecHash {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalySpecHashConflict})
	}
	if snapshot.Directory != nil && snapshot.Directory.Manifest != nil && snapshot.Directory.Manifest.SandboxID != snapshot.SandboxID {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalyIdentityConflict, Resource: "directory"})
	}
	if snapshot.Egress != nil && snapshot.Main.SchemaVersion != snapshot.Egress.SchemaVersion ||
		snapshot.Workspace != nil && snapshot.Main.SchemaVersion != snapshot.Workspace.SchemaVersion {
		snapshot.Anomalies = append(snapshot.Anomalies, ActualAnomaly{Code: ActualAnomalySchemaConflict})
	}
}

func cloneContainerObservation(source runtimeport.ManagedContainerObservation) runtimeport.ManagedContainerObservation {
	copy := source
	copy.CapAdd = append([]string(nil), source.CapAdd...)
	copy.CapDrop = append([]string(nil), source.CapDrop...)
	return copy
}

func cloneDirectoryObservation(source runtimeport.RuntimeDirectoryObservation) runtimeport.RuntimeDirectoryObservation {
	copy := source
	if source.Manifest != nil {
		manifest := *source.Manifest
		copy.Manifest = &manifest
	}
	return copy
}

func appendDamaged(anomalies []ActualAnomaly, resource, detail string) []ActualAnomaly {
	if detail == "" {
		detail = runtimeport.DiscoveryLabelsInvalid
	}
	return append(anomalies, ActualAnomaly{Code: ActualAnomalyResourceDamaged, Resource: resource, Detail: detail})
}

func sortAnomalies(anomalies []ActualAnomaly) {
	sort.SliceStable(anomalies, func(left, right int) bool {
		if anomalies[left].Code != anomalies[right].Code {
			return anomalies[left].Code < anomalies[right].Code
		}
		if anomalies[left].Resource != anomalies[right].Resource {
			return anomalies[left].Resource < anomalies[right].Resource
		}
		return anomalies[left].Detail < anomalies[right].Detail
	})
}
