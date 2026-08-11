package docker

import (
	"context"
	"sort"
	"strings"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

// ListManaged 枚举 schema version 1 的受管容器及独立损坏诊断项。
//
// 查询包含 stopped container。单个容器 labels、名称或状态损坏不会中断
// 其他结果；诊断项只携带安全代码，不回显原始 labels、状态或宿主机路径。
func (r *Runtime) ListManaged(
	ctx context.Context,
) ([]runtimeport.ActualSandbox, error) {
	filters := make(mobyclient.Filters).Add(
		"label",
		LabelManaged+"="+labelManagedValue,
	)
	result, err := r.engine.ContainerList(
		ctx,
		mobyclient.ContainerListOptions{
			All:     true,
			Filters: filters,
		},
	)
	if err != nil {
		return nil, runtimeUnavailable(err)
	}

	actual := make([]runtimeport.ActualSandbox, 0, len(result.Items))
	for _, container := range result.Items {
		actual = append(actual, mapManagedSummary(container))
	}
	sort.Slice(actual, func(left, right int) bool {
		if actual[left].ID != actual[right].ID {
			return actual[left].ID < actual[right].ID
		}
		return actual[left].RuntimeID < actual[right].RuntimeID
	})
	return actual, nil
}

// mapManagedSummary 把单个 Docker summary 映射为恢复模型或安全诊断项。
func mapManagedSummary(
	container mobycontainer.Summary,
) runtimeport.ActualSandbox {
	item := runtimeport.ActualSandbox{RuntimeID: container.ID}
	if validSandboxID(container.Labels[LabelSandboxID]) {
		item.ID = container.Labels[LabelSandboxID]
	}
	if container.Labels[LabelSchemaVersion] != labelSchemaVersionV1 && container.Labels[LabelSchemaVersion] != labelSchemaVersionValue {
		item.DiscoveryIssue = runtimeport.DiscoverySchemaUnsupported
		return item
	}
	metadata, err := ParseLabels(container.Labels)
	if err != nil || !summaryHasExpectedName(container.Names, metadata.SandboxID) {
		item.DiscoveryIssue = runtimeport.DiscoveryLabelsInvalid
		return item
	}
	state, err := mapSummaryState(container.State)
	if err != nil {
		item.ID = metadata.SandboxID
		item.DiscoveryIssue = runtimeport.DiscoveryStateUnsupported
		return item
	}
	return runtimeport.ActualSandbox{
		ID:                    metadata.SandboxID,
		RuntimeID:             container.ID,
		State:                 state,
		SpecHash:              metadata.SpecHash,
		Workspace:             metadata.Workspace,
		RunnerProtocolVersion: metadata.RunnerProtocolVersion,
	}
}

// summaryHasExpectedName 验证 Docker names 中包含唯一确定性容器名。
func summaryHasExpectedName(names []string, sandboxID string) bool {
	expected := containerName(sandboxID)
	for _, name := range names {
		if strings.TrimPrefix(name, "/") == expected {
			return true
		}
	}
	return false
}

// mapSummaryState 把 list summary 状态映射为稳定 runtime 四态。
func mapSummaryState(state mobycontainer.ContainerState) (runtimeport.ActualState, error) {
	switch state {
	case mobycontainer.StateCreated:
		return runtimeport.ActualCreated, nil
	case mobycontainer.StateRunning:
		return runtimeport.ActualRunning, nil
	case mobycontainer.StateExited, mobycontainer.StateDead:
		return runtimeport.ActualStopped, nil
	default:
		return "", containerStateConflict()
	}
}
