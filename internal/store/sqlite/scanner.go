package sqlite

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// selectSandboxByIDQuery 固定 Get 与事务内 CAS 回读使用的列顺序。
//
// 修改 schema 或 scanner 字段时必须同步修改本查询，避免不同读取路径对同一
// 记录产生不同解释。
const selectSandboxByIDQuery = `SELECT
	id,
	spec_json,
	desired_state,
	observed_state,
	reason,
	message,
	runtime_id,
	spec_hash,
	revision,
	created_at,
	updated_at,
	last_transition_at
FROM sandboxes
WHERE id = ?`

// rowScanner 抽象 sql.Row 和 sql.Rows 共有的 Scan 能力。
//
// 后续 Get、CAS 更新返回值和列表查询必须复用同一还原路径，避免不同 Store
// 方法对状态、时间或损坏数据产生不一致解释。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSandbox 从固定列顺序的查询结果完整还原领域 Sandbox。
//
// 本函数不信任数据库内容：未知枚举、损坏 JSON、非法 revision 和非法时间
// 统一返回 store.ErrCorrupt，且错误文本不回显原始字段值。
func scanSandbox(row rowScanner) (domain.Sandbox, error) {
	var (
		id                   string
		specJSON             []byte
		desiredState         string
		observedState        string
		reason               string
		message              string
		runtimeID            string
		specHash             string
		revision             int64
		createdAtText        string
		updatedAtText        string
		lastTransitionAtText string
	)
	if err := row.Scan(
		&id,
		&specJSON,
		&desiredState,
		&observedState,
		&reason,
		&message,
		&runtimeID,
		&specHash,
		&revision,
		&createdAtText,
		&updatedAtText,
		&lastTransitionAtText,
	); err != nil {
		return domain.Sandbox{}, err
	}

	spec, err := decodeStoredSpec(specJSON)
	if err != nil {
		return domain.Sandbox{}, err
	}
	desired, err := parseDesiredState(desiredState)
	if err != nil {
		return domain.Sandbox{}, err
	}
	observed, err := parseObservedState(observedState)
	if err != nil {
		return domain.Sandbox{}, err
	}
	if revision < 1 {
		return domain.Sandbox{}, corruptField("revision")
	}
	createdAt, err := parseStoredTime("created_at", createdAtText)
	if err != nil {
		return domain.Sandbox{}, err
	}
	updatedAt, err := parseStoredTime("updated_at", updatedAtText)
	if err != nil {
		return domain.Sandbox{}, err
	}
	lastTransitionAt, err := parseStoredTime(
		"last_transition_at",
		lastTransitionAtText,
	)
	if err != nil {
		return domain.Sandbox{}, err
	}

	return domain.Sandbox{
		ID:               id,
		Spec:             spec,
		DesiredState:     desired,
		ObservedState:    observed,
		Reason:           reason,
		Message:          message,
		RuntimeID:        runtimeID,
		SpecHash:         specHash,
		Revision:         uint64(revision),
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		LastTransitionAt: lastTransitionAt,
		ExpiresAt:        nil,
	}, nil
}

// decodeStoredSpec 解码 SQLite 私有 JSON 结构并显式转换为领域规格。
func decodeStoredSpec(data []byte) (domain.SandboxSpec, error) {
	var stored storedSandboxSpec
	if err := json.Unmarshal(data, &stored); err != nil {
		return domain.SandboxSpec{}, corruptField("spec_json")
	}
	spec := domain.SandboxSpec{
		Image: stored.Image,
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: stored.Resources.CPUQuotaMillis,
			MemoryMiB:      stored.Resources.MemoryMiB,
			PIDs:           stored.Resources.PIDs,
		},
		Workspace: domain.WorkspaceSpec{
			MountPath:  stored.Workspace.MountPath,
			Persistent: stored.Workspace.Persistent,
		},
		Network: domain.NetworkSpec{
			Outbound: stored.Network.Outbound,
		},
		Platform: domain.Platform{
			OS:   stored.Platform.OS,
			Arch: stored.Platform.Arch,
		},
	}
	// 当前配置上限可能在记录创建后收紧，恢复旧记录时不能用新配置误判
	// 损坏；这里使用 int64 最大值只检查领域结构和 Phase 1 固定不变量。
	if err := spec.Validate(domain.ResourceBounds{
		MaxCPUQuotaMillis: math.MaxInt64,
		MaxMemoryMiB:      math.MaxInt64,
		MaxPIDs:           math.MaxInt64,
	}); err != nil {
		return domain.SandboxSpec{}, corruptField("spec_json")
	}
	return spec, nil
}

// parseDesiredState 校验持久化的期望状态属于当前 schema 支持的集合。
func parseDesiredState(value string) (domain.DesiredState, error) {
	state := domain.DesiredState(value)
	switch state {
	case domain.DesiredRunning, domain.DesiredTerminated:
		return state, nil
	default:
		return "", corruptField("desired_state")
	}
}

// parseObservedState 校验持久化的观测状态属于当前 schema 支持的集合。
func parseObservedState(value string) (domain.SandboxState, error) {
	state := domain.SandboxState(value)
	switch state {
	case domain.StatePending,
		domain.StateCreating,
		domain.StateRunning,
		domain.StateStopping,
		domain.StateTerminated,
		domain.StateFailed:
		return state, nil
	default:
		return "", corruptField("observed_state")
	}
}

// parseStoredTime 解析 RFC3339Nano 时间并归一化为 UTC。
func parseStoredTime(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, corruptField(field)
	}
	return parsed.UTC(), nil
}

// corruptField 构造不包含损坏字段原值的可分类错误。
func corruptField(field string) error {
	return fmt.Errorf("%w: invalid %s", storeport.ErrCorrupt, field)
}
