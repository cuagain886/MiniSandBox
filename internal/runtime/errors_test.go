package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"minisandbox/internal/runnerclient"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/runtime/docker"
)

// TestClassifyErrorCoversEveryFailureReason 验证 Phase 1 全部失败 reason 和 retryable。
func TestClassifyErrorCoversEveryFailureReason(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		reason    string
		retryable bool
	}{
		{
			name:      "image pull",
			err:       &docker.ImagePullFailedError{},
			reason:    runtimeport.FailureReasonImagePullFailed,
			retryable: true,
		},
		{
			name:   "artifact invalid",
			err:    &docker.ArtifactInvalidError{},
			reason: runtimeport.FailureReasonArtifactInvalid,
		},
		{
			name:      "container create",
			err:       &docker.ContainerCreateFailedError{},
			reason:    runtimeport.FailureReasonContainerCreateFailed,
			retryable: true,
		},
		{
			name:      "artifact injection",
			err:       &docker.ArtifactInjectionFailedError{},
			reason:    runtimeport.FailureReasonArtifactInjectionFailed,
			retryable: true,
		},
		{
			name:      "container start",
			err:       &docker.ContainerStartFailedError{},
			reason:    runtimeport.FailureReasonContainerStartFailed,
			retryable: true,
		},
		{
			name:      "runner unhealthy",
			err:       &runnerclient.UnhealthyError{},
			reason:    runtimeport.FailureReasonRunnerUnhealthy,
			retryable: true,
		},
		{
			name:   "spec drift",
			err:    &docker.SpecDriftError{},
			reason: runtimeport.FailureReasonSpecDrift,
		},
		{
			name:      "cleanup pending",
			err:       &docker.CleanupPendingError{},
			reason:    runtimeport.FailureReasonCleanupPending,
			retryable: true,
		},
		{
			name:      "runtime unavailable",
			err:       &docker.RuntimeUnavailableError{},
			reason:    runtimeport.FailureReasonRuntimeUnavailable,
			retryable: true,
		},
		{
			name:      "internal",
			err:       errors.New("unknown"),
			reason:    runtimeport.FailureReasonInternalError,
			retryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure := runtimeport.ClassifyError(
				errors.Join(errors.New("outer context"), tt.err),
			)
			if failure.Reason != tt.reason ||
				failure.Retryable != tt.retryable ||
				failure.Message == "" {
				t.Fatalf("failure: got %#v", failure)
			}
		})
	}
}

// TestClassifyErrorMapsDeadlineToRuntimeUnavailable 验证 deadline 不依赖错误文本。
func TestClassifyErrorMapsDeadlineToRuntimeUnavailable(t *testing.T) {
	failure := runtimeport.ClassifyError(
		errors.Join(errors.New("operation"), context.DeadlineExceeded),
	)
	if failure.Reason != runtimeport.FailureReasonRuntimeUnavailable ||
		!failure.Retryable {
		t.Fatalf("failure: %#v", failure)
	}
}

// TestClassifyErrorNeverLeaksCause 验证已知与未知 cause 都只返回固定公共 message。
func TestClassifyErrorNeverLeaksCause(t *testing.T) {
	const secret = "secret socket path and registry credential"
	for _, err := range []error{
		&classifiedSecretError{secret: secret},
		errors.New(secret),
	} {
		failure := runtimeport.ClassifyError(err)
		if strings.Contains(failure.Message, secret) ||
			strings.Contains(failure.Reason, secret) {
			t.Fatalf("classification leaked cause: %#v", failure)
		}
	}
}

// TestClassifyErrorNilReturnsZero 验证无错误不会伪造失败状态。
func TestClassifyErrorNilReturnsZero(t *testing.T) {
	if failure := runtimeport.ClassifyError(nil); failure != (runtimeport.Failure{}) {
		t.Fatalf("nil classification: %#v", failure)
	}
}

// classifiedSecretError 模拟带受支持 marker 但 Error 文本包含秘密的 cause。
type classifiedSecretError struct {
	secret string
}

// Error 返回测试秘密，classifier 不得读取该文本生成 message。
func (e *classifiedSecretError) Error() string {
	return e.secret
}

// FailureReason 返回受支持 reason，验证 allowlist 固定 message。
func (*classifiedSecretError) FailureReason() string {
	return runtimeport.FailureReasonContainerCreateFailed
}
