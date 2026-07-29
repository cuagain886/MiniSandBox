package docker

import (
	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// SpecDriftError 表示已有 Docker 资源身份或规格与持久化记录不一致。
type SpecDriftError struct {
	cause error
}

// Error 返回不回显 labels、名称或 spec 内容的固定文案。
func (*SpecDriftError) Error() string {
	return "sandbox runtime specification has drifted"
}

// Unwrap 保留具体冲突 cause；没有 cause 时返回领域 conflict。
func (e *SpecDriftError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return domain.ErrConflict
}

// FailureReason 返回稳定的 spec drift 生命周期 reason。
func (*SpecDriftError) FailureReason() string {
	return runtimeport.FailureReasonSpecDrift
}

// ContainerCreateFailedError 表示 Docker ContainerCreate 阶段失败。
type ContainerCreateFailedError struct {
	cause error
}

// Error 返回不包含 daemon 响应的固定文案。
func (*ContainerCreateFailedError) Error() string {
	return "sandbox container creation failed"
}

// Unwrap 返回内部 create cause。
func (e *ContainerCreateFailedError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 container create 生命周期 reason。
func (*ContainerCreateFailedError) FailureReason() string {
	return runtimeport.FailureReasonContainerCreateFailed
}

// ArtifactInjectionFailedError 表示 Docker CopyToContainer 阶段失败。
type ArtifactInjectionFailedError struct {
	cause error
}

// Error 返回不包含路径、archive 内容或 daemon 响应的固定文案。
func (*ArtifactInjectionFailedError) Error() string {
	return "sandbox artifact injection failed"
}

// Unwrap 返回内部 copy cause。
func (e *ArtifactInjectionFailedError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 artifact injection 生命周期 reason。
func (*ArtifactInjectionFailedError) FailureReason() string {
	return runtimeport.FailureReasonArtifactInjectionFailed
}

// ContainerStartFailedError 表示准备完成的 container 无法启动。
type ContainerStartFailedError struct {
	cause error
}

// Error 返回不包含容器 ID、状态或 daemon 响应的固定文案。
func (*ContainerStartFailedError) Error() string {
	return "sandbox container start failed"
}

// Unwrap 返回内部 start cause。
func (e *ContainerStartFailedError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 container start 生命周期 reason。
func (*ContainerStartFailedError) FailureReason() string {
	return runtimeport.FailureReasonContainerStartFailed
}
