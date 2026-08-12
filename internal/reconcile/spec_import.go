package reconcile

import (
	"errors"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// TrustedSpecError 表示实际 bundle 无法安全重建为领域规格；错误不包含实际字段值。
type TrustedSpecError struct {
	code string
}

// Error 返回固定安全错误文本。
func (e *TrustedSpecError) Error() string {
	return "actual resource bundle cannot reconstruct a trusted spec"
}

// Code 返回稳定的失败分类，不返回镜像、路径或 Docker 原始数据。
func (e *TrustedSpecError) Code() string { return e.code }

const (
	trustedSpecBundleInvalid  = "BUNDLE_INVALID"
	trustedSpecProfileInvalid = "PROFILE_INVALID"
	trustedSpecHashMismatch   = "HASH_MISMATCH"
)

// RebuildResolvedSpec 从 allowlist observation 重建完整 SandboxSpec，并验证全部安全 profile 与摘要。
// 本函数不读取容器 env/command、不访问 Store，也不透传未知 inspect 字段。
func RebuildResolvedSpec(actual ActualResourceSnapshot, expected DriftExpectation) (domain.SandboxSpec, error) {
	if actual.SandboxID == "" || actual.Main == nil || actual.Workspace == nil || actual.Directory == nil ||
		!actual.Directory.DirectoryPresent || len(actual.Anomalies) != 0 {
		return domain.SandboxSpec{}, trustedSpecError(trustedSpecBundleInvalid)
	}
	main := actual.Main
	if main.SandboxID != actual.SandboxID || main.Role != runtimeport.ContainerRoleMain || main.ImageReference == "" ||
		len(main.ImageReference) > domain.MaxImageReferenceLength || main.PlatformOS != "linux" || main.PlatformArch != "amd64" ||
		!main.ResourceProfileValid || main.CPUQuotaMillis <= 0 || main.MemoryMiB <= 0 || main.PIDs <= 0 ||
		main.WorkspaceDestination != domain.WorkspaceMountPath || main.Workspace != actual.Workspace.VolumeName {
		return domain.SandboxSpec{}, trustedSpecError(trustedSpecProfileInvalid)
	}
	outbound := false
	switch main.NetworkMode {
	case "none":
		if actual.Egress != nil {
			return domain.SandboxSpec{}, trustedSpecError(trustedSpecProfileInvalid)
		}
	case "container":
		outbound = true
		if actual.Egress == nil {
			return domain.SandboxSpec{}, trustedSpecError(trustedSpecProfileInvalid)
		}
	default:
		return domain.SandboxSpec{}, trustedSpecError(trustedSpecProfileInvalid)
	}

	spec := domain.SandboxSpec{
		Image: main.ImageReference,
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: main.CPUQuotaMillis,
			MemoryMiB:      main.MemoryMiB,
			PIDs:           main.PIDs,
		},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath, Persistent: false},
		Network:   domain.NetworkSpec{Outbound: outbound},
		Platform:  domain.Platform{OS: "linux", Arch: "amd64"},
	}
	if spec.Hash() != main.SpecHash || actual.Workspace.SpecHash != main.SpecHash ||
		actual.Directory.Manifest != nil && actual.Directory.Manifest.SpecHash != main.SpecHash {
		return domain.SandboxSpec{}, trustedSpecError(trustedSpecHashMismatch)
	}
	// 复用 drift comparator 校验固定安全 profile、协议、policy、netns 和跨资源身份，
	// 防止 importer 与正常运行时漂移规则演化成两套安全边界。
	pseudoStore := domain.Sandbox{ID: actual.SandboxID, Spec: spec, SpecHash: main.SpecHash}
	if fields := CompareSandboxDrift(pseudoStore, actual, expected); len(fields) != 0 {
		return domain.SandboxSpec{}, trustedSpecError(trustedSpecProfileInvalid)
	}
	return spec, nil
}

func trustedSpecError(code string) error {
	return &TrustedSpecError{code: code}
}

// TrustedSpecErrorCode 提取安全失败码；非 importer 错误返回空字符串。
func TrustedSpecErrorCode(err error) string {
	var target *TrustedSpecError
	if errors.As(err, &target) {
		return target.Code()
	}
	return ""
}
