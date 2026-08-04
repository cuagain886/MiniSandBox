package docker

import (
	"errors"
	"strconv"
	"strings"

	"minisandbox/internal/runnerbootstrap"
)

const (
	// LabelManaged 标识资源由 MiniSandbox 控制面管理。
	LabelManaged = "minisandbox.io/managed"
	// LabelSandboxID 保存可用于重启恢复的 sandbox ID。
	LabelSandboxID = "minisandbox.io/id"
	// LabelSchemaVersion 标识 labels 恢复协议版本。
	LabelSchemaVersion = "minisandbox.io/schema-version"
	// LabelSpecHash 保存安全规格的摘要，不保存规格明文或秘密。
	LabelSpecHash = "minisandbox.io/spec-hash"
	// LabelExpiresAt 保存用于恢复 TTL 的到期时间；Phase 1 固定为空。
	LabelExpiresAt = "minisandbox.io/expires-at"
	// LabelWorkspace 保存受管 workspace volume 的确定性名称。
	LabelWorkspace = "minisandbox.io/workspace"
	// LabelRunnerProtocolVersion 保存容器内 runner 的非秘密整数协议版本。
	LabelRunnerProtocolVersion = "minisandbox.io/runner-protocol-version"

	labelManagedValue       = "true"
	labelSchemaVersionValue = "1"
	maxLabelValueLength     = 256
	maxWorkspaceNameLength  = 128
)

var managedLabelKeys = [...]string{
	LabelManaged,
	LabelSandboxID,
	LabelSchemaVersion,
	LabelSpecHash,
	LabelExpiresAt,
	LabelWorkspace,
	LabelRunnerProtocolVersion,
}

// ManagedLabels 是恢复 sandbox 所需的安全 Docker label 元数据。
//
// schema、managed 和 expires-at 在 Phase 1 由 codec 固定，调用方不能注入；
// 本类型故意不包含 token、命令、环境变量、镜像凭据或宿主机路径。
type ManagedLabels struct {
	// SandboxID 是控制面生成的规范 UUID v4。
	SandboxID string
	// SpecHash 是 resolved spec 的 64 位小写十六进制 SHA-256。
	SpecHash string
	// Workspace 是受管 named volume 的确定性名称。
	Workspace string
	// RunnerProtocolVersion 是容器声明的 runner 内部协议版本；编码时零值表示
	// 使用当前版本，解析结果始终是经过精确匹配的当前版本。
	RunnerProtocolVersion int
}

// EncodeLabels 校验恢复元数据并生成 schema version 1 的完整 label 集合。
func EncodeLabels(metadata ManagedLabels) (map[string]string, error) {
	if err := validateManagedLabels(metadata); err != nil {
		return nil, err
	}
	return map[string]string{
		LabelManaged:       labelManagedValue,
		LabelSandboxID:     metadata.SandboxID,
		LabelSchemaVersion: labelSchemaVersionValue,
		LabelSpecHash:      metadata.SpecHash,
		LabelExpiresAt:     "",
		LabelWorkspace:     metadata.Workspace,
		LabelRunnerProtocolVersion: strconv.Itoa(
			runnerbootstrap.CurrentProtocolVersion,
		),
	}, nil
}

// ParseLabels 解析并验证 schema version 1 的受管资源 labels。
//
// Docker 资源可能同时带有镜像或运维系统添加的其他 labels，因此只读取本
// codec 管理的固定键；任何错误都只报告字段名，不回显潜在恶意值。
func ParseLabels(labels map[string]string) (ManagedLabels, error) {
	for _, key := range managedLabelKeys {
		value, ok := labels[key]
		if !ok {
			return ManagedLabels{}, labelError(key, "is required")
		}
		if len(value) > maxLabelValueLength {
			return ManagedLabels{}, labelError(key, "is too long")
		}
	}
	if labels[LabelManaged] != labelManagedValue {
		return ManagedLabels{}, labelError(LabelManaged, "must identify a managed resource")
	}
	if labels[LabelSchemaVersion] != labelSchemaVersionValue {
		return ManagedLabels{}, labelError(LabelSchemaVersion, "is not supported")
	}
	if labels[LabelExpiresAt] != "" {
		return ManagedLabels{}, labelError(LabelExpiresAt, "must be empty in Phase 1")
	}
	protocolVersion, err := strconv.Atoi(labels[LabelRunnerProtocolVersion])
	if err != nil || protocolVersion != runnerbootstrap.CurrentProtocolVersion {
		return ManagedLabels{}, labelError(
			LabelRunnerProtocolVersion,
			"is not supported",
		)
	}

	metadata := ManagedLabels{
		SandboxID:             labels[LabelSandboxID],
		SpecHash:              labels[LabelSpecHash],
		Workspace:             labels[LabelWorkspace],
		RunnerProtocolVersion: protocolVersion,
	}
	if err := validateManagedLabels(metadata); err != nil {
		return ManagedLabels{}, err
	}
	return metadata, nil
}

// validateManagedLabels 校验所有可变恢复字段，不接触 Docker。
func validateManagedLabels(metadata ManagedLabels) error {
	if metadata.RunnerProtocolVersion != 0 && metadata.RunnerProtocolVersion != runnerbootstrap.CurrentProtocolVersion {
		return labelError(LabelRunnerProtocolVersion, "is not supported")
	}
	if !validSandboxID(metadata.SandboxID) {
		return labelError(LabelSandboxID, "is not a canonical UUID v4")
	}
	if !validLowerHex(metadata.SpecHash, 64) {
		return labelError(LabelSpecHash, "is not a SHA-256 digest")
	}
	if !validResourceName(metadata.Workspace, maxWorkspaceNameLength) {
		return labelError(LabelWorkspace, "is not a safe resource name")
	}
	if metadata.Workspace != workspaceName(metadata.SandboxID) {
		return labelError(LabelWorkspace, "does not match the sandbox ID")
	}
	return nil
}

// validSandboxID 只接受控制面生成的规范小写 UUID v4。
func validSandboxID(id string) bool {
	if len(id) != 36 ||
		id[8] != '-' ||
		id[13] != '-' ||
		id[18] != '-' ||
		id[23] != '-' ||
		id[14] != '4' {
		return false
	}
	for index := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !isLowerHex(id[index]) {
			return false
		}
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// validLowerHex 检查字符串是否为固定长度的小写十六进制。
func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := range value {
		if !isLowerHex(value[index]) {
			return false
		}
	}
	return true
}

// isLowerHex 判断单字节是否属于小写十六进制集合。
func isLowerHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}

// validResourceName 验证 Docker 资源标识只含安全 ASCII 字符。
func validResourceName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			index > 0 && strings.ContainsRune("_.-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

// labelError 创建不回显原始 label 值的安全校验错误。
func labelError(field, problem string) error {
	return errors.New(field + " " + problem)
}
