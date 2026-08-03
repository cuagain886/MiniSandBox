package domain

import "strconv"

// MaxImageReferenceLength 是 Phase 1 允许的镜像引用最大字节数。
//
// 该上限用于在进入 runtime 前拒绝明显异常的超长引用;registry allowlist
// 等更细的镜像策略不在领域校验范围内。
const MaxImageReferenceLength = 512

// WorkspaceMountPath 是 Phase 1 固定的容器内工作目录挂载路径。
const WorkspaceMountPath = "/workspace"

// ResourceBounds 描述服务端允许的每 sandbox 资源上限。
//
// 各字段单位与 ResourceLimits 一致;取值必须为正数,由配置层在调用校验前
// 完成自身合法性检查。零值 bounds 会使所有资源请求校验失败,属于安全的
// 拒绝行为而不是放行。
type ResourceBounds struct {
	// MaxCPUQuotaMillis 是允许申请的最大 CPU 毫核数。
	MaxCPUQuotaMillis int64
	// MaxMemoryMiB 是允许申请的最大内存 MiB 数。
	MaxMemoryMiB int64
	// MaxPIDs 是允许申请的最大进程数上限。
	MaxPIDs int64
}

// SpecFieldError 表示 resolved spec 中单个字段违反当前阶段校验规则。
//
// Field 使用稳定的字段路径(如 spec.image)定位问题;Message 只描述规则
// 本身,绝不回显字段值,避免镜像引用等潜在敏感内容进入日志或 API 响应。
type SpecFieldError struct {
	// Field 是违规字段的稳定路径,供错误映射与诊断定位使用。
	Field string
	// Message 是不含字段值的安全规则说明。
	Message string
}

// Error 返回 "字段路径: 规则说明" 形式的诊断文本。
func (e *SpecFieldError) Error() string {
	return e.Field + ": " + e.Message
}

// Unwrap 使校验错误满足 errors.Is(err, ErrInvalid),便于上层统一映射。
func (e *SpecFieldError) Unwrap() error {
	return ErrInvalid
}

// Validate 按当前阶段规则校验 resolved spec,返回第一处违规。
//
// 校验发生在期望状态持久化之前;bounds 来自服务端配置,不由客户端提供。
// 返回的错误只定位字段并说明规则,不携带字段原始值。
func (s SandboxSpec) Validate(bounds ResourceBounds) error {
	if s.Image == "" {
		return &SpecFieldError{
			Field:   "spec.image",
			Message: "image reference must not be empty",
		}
	}
	if len(s.Image) > MaxImageReferenceLength {
		return &SpecFieldError{
			Field: "spec.image",
			Message: "image reference must not exceed " +
				strconv.Itoa(MaxImageReferenceLength) + " bytes",
		}
	}
	if s.Resources.CPUQuotaMillis < 1 ||
		s.Resources.CPUQuotaMillis > bounds.MaxCPUQuotaMillis {
		return &SpecFieldError{
			Field:   "spec.resources.cpu_quota_millis",
			Message: "cpu quota is outside the allowed server range",
		}
	}
	if s.Resources.MemoryMiB < 1 ||
		s.Resources.MemoryMiB > bounds.MaxMemoryMiB {
		return &SpecFieldError{
			Field:   "spec.resources.memory_mib",
			Message: "memory limit is outside the allowed server range",
		}
	}
	if s.Resources.PIDs < 1 || s.Resources.PIDs > bounds.MaxPIDs {
		return &SpecFieldError{
			Field:   "spec.resources.pids",
			Message: "pids limit is outside the allowed server range",
		}
	}
	if s.Workspace.MountPath != WorkspaceMountPath {
		return &SpecFieldError{
			Field:   "spec.workspace.mount_path",
			Message: "mount path must be " + WorkspaceMountPath,
		}
	}
	if s.Workspace.Persistent {
		return &SpecFieldError{
			Field:   "spec.workspace.persistent",
			Message: "persistent workspace is not supported in Phase 1",
		}
	}
	if s.Platform.OS != "linux" {
		return &SpecFieldError{
			Field:   "spec.platform.os",
			Message: "only linux is supported in Phase 1",
		}
	}
	if s.Platform.Arch != "amd64" {
		return &SpecFieldError{
			Field:   "spec.platform.arch",
			Message: "only amd64 is supported in Phase 1",
		}
	}
	return nil
}
