package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"minisandbox/internal/domain"
	"minisandbox/internal/egresspolicy"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

// DriftField 是不含实际值的固定安全漂移字段码。
type DriftField string

const (
	// DriftActualBundle 表示实际资源缺失、重复或包含下层 anomaly。
	DriftActualBundle DriftField = "actual.bundle"
	// DriftSpecHash 表示 Store、label 或领域规格重算摘要不一致。
	DriftSpecHash DriftField = "spec.hash"
	// DriftImage 表示主容器镜像引用与 Store 不一致。
	DriftImage DriftField = "spec.image"
	// DriftPlatform 表示主容器平台与 Store 不一致。
	DriftPlatform DriftField = "spec.platform"
	// DriftCPU 表示 CPU 上限不可逆映射或与 Store 不一致。
	DriftCPU DriftField = "spec.resources.cpu"
	// DriftMemory 表示内存上限不可逆映射或与 Store 不一致。
	DriftMemory DriftField = "spec.resources.memory"
	// DriftPIDs 表示进程数上限缺失或与 Store 不一致。
	DriftPIDs DriftField = "spec.resources.pids"
	// DriftWorkspace 表示 workspace 卷或容器内目标不一致。
	DriftWorkspace DriftField = "spec.workspace"
	// DriftNetwork 表示 outbound 规格与实际网络形态不一致。
	DriftNetwork DriftField = "spec.network"
	// DriftName 表示实际资源身份未匹配确定性名称。
	DriftName DriftField = "runtime.name"
	// DriftRole 表示 main/egress resource role 不一致。
	DriftRole DriftField = "runtime.role"
	// DriftProcessProfile 表示用户、入口点或工作目录不一致。
	DriftProcessProfile DriftField = "security.process"
	// DriftMountProfile 表示发现额外挂载、任意 bind 或 socket 暴露。
	DriftMountProfile DriftField = "security.mounts"
	// DriftNamespaceProfile 表示发现 host namespace 扩权。
	DriftNamespaceProfile DriftField = "security.namespaces"
	// DriftPortProfile 表示发现声明或发布端口。
	DriftPortProfile DriftField = "security.ports"
	// DriftDeviceProfile 表示发现设备或 volumes-from 扩权。
	DriftDeviceProfile DriftField = "security.devices"
	// DriftPrivilegeProfile 表示 privileged、capability 或 no-new-privileges 不一致。
	DriftPrivilegeProfile DriftField = "security.privileges"
	// DriftRestartPolicy 表示容器 restart policy 不是禁用状态。
	DriftRestartPolicy DriftField = "security.restart_policy"
	// DriftRunnerProtocol 表示主容器 runner 协议不一致。
	DriftRunnerProtocol DriftField = "protocol.runner"
	// DriftEgressProtocol 表示 sidecar 协议不一致。
	DriftEgressProtocol DriftField = "protocol.egress"
	// DriftEgressPolicy 表示 sidecar policy 摘要与服务端期望不一致。
	DriftEgressPolicy DriftField = "egress.policy"
	// DriftNetNS 表示 main 未共享可信 sidecar 的 network namespace。
	DriftNetNS DriftField = "egress.netns"
)

// DriftExpectation 保存由服务端安全配置导出的比较值，不接受 sandbox 请求覆盖。
type DriftExpectation struct {
	// EgressPolicyHash 是 outbound sidecar 应执行的规范化策略摘要；非 outbound 时为空。
	EgressPolicyHash string
}

// CompareSandboxDrift 比较 Store 权威规格与只读实际 bundle，并返回去重稳定排序的字段码。
func CompareSandboxDrift(stored domain.Sandbox, actual ActualResourceSnapshot, expected DriftExpectation) []DriftField {
	fields := map[DriftField]struct{}{}
	add := func(condition bool, field DriftField) {
		if condition {
			fields[field] = struct{}{}
		}
	}
	add(stored.ID != actual.SandboxID || len(actual.Anomalies) != 0 || actual.Main == nil, DriftActualBundle)
	if actual.Main == nil {
		return sortedDriftFields(fields)
	}
	main := actual.Main
	add(main.SandboxID != stored.ID, DriftName)
	add(main.Role != runtimeport.ContainerRoleMain, DriftRole)
	add(main.SpecHash != stored.SpecHash || stored.Spec.Hash() != stored.SpecHash, DriftSpecHash)
	add(main.ImageReference != stored.Spec.Image, DriftImage)
	add(main.PlatformOS != stored.Spec.Platform.OS || main.PlatformArch != stored.Spec.Platform.Arch, DriftPlatform)
	add(!main.ResourceProfileValid || main.CPUQuotaMillis != stored.Spec.Resources.CPUQuotaMillis, DriftCPU)
	add(!main.ResourceProfileValid || main.MemoryMiB != stored.Spec.Resources.MemoryMiB, DriftMemory)
	add(!main.ResourceProfileValid || main.PIDs != stored.Spec.Resources.PIDs, DriftPIDs)
	add(actual.Workspace == nil || actual.Workspace != nil && main.Workspace != actual.Workspace.VolumeName || main.WorkspaceDestination != stored.Spec.Workspace.MountPath || stored.Spec.Workspace.Persistent, DriftWorkspace)
	add(main.ProcessProfileValid == false, DriftProcessProfile)
	add(main.MountProfileValid == false, DriftMountProfile)
	add(main.NamespaceProfileValid == false, DriftNamespaceProfile)
	add(main.PortProfileValid == false, DriftPortProfile)
	add(main.DeviceProfileValid == false, DriftDeviceProfile)
	add(main.Privileged || !main.NoNewPrivileges || main.ReadonlyRootfs || !sameDriftSet(main.CapDrop, "ALL") || !sameDriftSet(main.CapAdd, "CHOWN", "SETUID", "SETGID", "KILL"), DriftPrivilegeProfile)
	add(main.RestartPolicy != "no", DriftRestartPolicy)
	add(main.RunnerProtocolVersion != runnerbootstrap.CurrentProtocolVersion, DriftRunnerProtocol)

	outbound := main.NetworkMode == "container"
	add(outbound != stored.Spec.Network.Outbound, DriftNetwork)
	if stored.Spec.Network.Outbound {
		if actual.Egress == nil {
			add(true, DriftActualBundle)
		} else {
			egress := actual.Egress
			add(egress.Role != runtimeport.ContainerRoleEgress, DriftRole)
			add(egress.EgressProtocolVersion != egresspolicy.CurrentProtocolVersion, DriftEgressProtocol)
			add(expected.EgressPolicyHash == "" || egress.EgressPolicyHash != expected.EgressPolicyHash, DriftEgressPolicy)
			add(main.NetworkPeerContainerID != egress.ContainerID || egress.NetworkMode != "managed-egress", DriftNetNS)
			add(!egress.ProcessProfileValid, DriftProcessProfile)
			add(!egress.MountProfileValid, DriftMountProfile)
			add(!egress.NamespaceProfileValid, DriftNamespaceProfile)
			add(!egress.PortProfileValid, DriftPortProfile)
			add(!egress.DeviceProfileValid, DriftDeviceProfile)
			add(egress.Privileged || !egress.ReadonlyRootfs || !egress.NoNewPrivileges || !sameDriftSet(egress.CapDrop, "ALL") || !sameDriftSet(egress.CapAdd, "NET_ADMIN", "SETUID", "SETGID"), DriftPrivilegeProfile)
			add(egress.RestartPolicy != "no", DriftRestartPolicy)
		}
	} else {
		add(actual.Egress != nil, DriftNetwork)
	}
	return sortedDriftFields(fields)
}

func sortedDriftFields(fields map[DriftField]struct{}) []DriftField {
	result := make([]DriftField, 0, len(fields))
	for field := range fields {
		result = append(result, field)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func sameDriftSet(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		wanted[strings.TrimPrefix(strings.ToUpper(value), "CAP_")] = struct{}{}
	}
	seen := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		normalized := strings.TrimPrefix(strings.ToUpper(value), "CAP_")
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

const driftRecordCASAttempts = 3

// DriftRecordStore 是 SPEC_DRIFT 安全诊断持久化所需的最小 Store 能力。
type DriftRecordStore interface {
	Get(context.Context, string) (domain.Sandbox, error)
	UpdateObserved(context.Context, storeport.ObservedUpdate) (domain.Sandbox, error)
}

// RecordSpecDrift 在比较结果仍成立时 CAS 写入固定 SPEC_DRIFT 状态，不修改 runtime 或 Store spec。
func RecordSpecDrift(ctx context.Context, store DriftRecordStore, snapshot ActualResourceSnapshot, expected DriftExpectation) (domain.Sandbox, []DriftField, error) {
	if store == nil || snapshot.SandboxID == "" {
		return domain.Sandbox{}, nil, fmt.Errorf("record spec drift: %w", domain.ErrInvalid)
	}
	for attempt := 0; attempt < driftRecordCASAttempts; attempt++ {
		current, err := store.Get(ctx, snapshot.SandboxID)
		if err != nil {
			return domain.Sandbox{}, nil, fmt.Errorf("record spec drift: read store: %w", err)
		}
		fields := CompareSandboxDrift(current, snapshot, expected)
		if len(fields) == 0 {
			return current, nil, nil
		}
		if current.DesiredState != domain.DesiredRunning {
			return domain.Sandbox{}, fields, fmt.Errorf("record spec drift: delete intent has priority: %w", domain.ErrConflict)
		}
		message, _ := domain.SandboxReasonPublicMessage(domain.SandboxReasonSpecDrift)
		updated, err := store.UpdateObserved(ctx, storeport.ObservedUpdate{
			ID: current.ID, ExpectedRevision: current.Revision, State: domain.StateFailed,
			Reason: domain.SandboxReasonSpecDrift, Message: message, RuntimeID: current.RuntimeID,
		})
		if err == nil {
			return updated, fields, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return domain.Sandbox{}, fields, fmt.Errorf("record spec drift: commit: %w", err)
		}
	}
	return domain.Sandbox{}, nil, fmt.Errorf("record spec drift: CAS retry exhausted: %w", domain.ErrConflict)
}

var _ DriftRecordStore = (storeport.Store)(nil)
