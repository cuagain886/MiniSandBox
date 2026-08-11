package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	storeport "minisandbox/internal/store"
)

// TestClassifyRetryErrorRuntimeTypedErrors 验证全部已知 runtime reason 与 retryability 一致。
func TestClassifyRetryErrorRuntimeTypedErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		class  RetryErrorClass
		reason string
	}{
		{"image pull", &dockerruntime.ImagePullFailedError{}, RetryErrorTransient, runtimeport.FailureReasonImagePullFailed},
		{"artifact invalid", &dockerruntime.ArtifactInvalidError{}, RetryErrorPermanent, runtimeport.FailureReasonArtifactInvalid},
		{"container create", &dockerruntime.ContainerCreateFailedError{}, RetryErrorTransient, runtimeport.FailureReasonContainerCreateFailed},
		{"artifact injection", &dockerruntime.ArtifactInjectionFailedError{}, RetryErrorTransient, runtimeport.FailureReasonArtifactInjectionFailed},
		{"container start", &dockerruntime.ContainerStartFailedError{}, RetryErrorTransient, runtimeport.FailureReasonContainerStartFailed},
		{"spec drift", &dockerruntime.SpecDriftError{}, RetryErrorPermanent, runtimeport.FailureReasonSpecDrift},
		{"cleanup pending", &dockerruntime.CleanupPendingError{}, RetryErrorTransient, runtimeport.FailureReasonCleanupPending},
		{"runtime unavailable", &dockerruntime.RuntimeUnavailableError{}, RetryErrorTransient, runtimeport.FailureReasonRuntimeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyRetryError(RetryOperationCreate, fmt.Errorf("wrapped: %w", test.err))
			if got.ErrorClass != test.class || got.Reason != test.reason || got.Successful {
				t.Fatalf("classification=%#v", got)
			}
		})
	}
}

// TestClassifyRetryErrorRunnerAndDomainTypes 验证 runner、Store 与安全错误不依赖文本。
func TestClassifyRetryErrorRunnerAndDomainTypes(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class RetryErrorClass
	}{
		{"runner protocol", &runnerclient.ProtocolMismatchError{}, RetryErrorPermanent},
		{"runner auth", &runnerclient.AuthenticationError{}, RetryErrorPermanent},
		{"runner connection", &runnerclient.ConnectionError{}, RetryErrorTransient},
		{"runner socket", &runnerclient.SocketMissingError{}, RetryErrorTransient},
		{"runner unhealthy", &runnerclient.UnhealthyError{}, RetryErrorTransient},
		{"runner timeout", &runnerclient.TimeoutError{}, RetryErrorTransient},
		{"runner 503", &runnerclient.StatusError{StatusCode: 503}, RetryErrorTransient},
		{"runner 401", &runnerclient.StatusError{StatusCode: 401}, RetryErrorPermanent},
		{"store corrupt", storeport.ErrCorrupt, RetryErrorPermanent},
		{"egress policy", domain.ErrEgressPolicyInvalid, RetryErrorPermanent},
		{"egress not ready", domain.ErrEgressNotReady, RetryErrorTransient},
		{"deadline", context.DeadlineExceeded, RetryErrorTransient},
		{"runtime missing", &dockerruntime.RuntimeMissingError{}, RetryErrorTransient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyRetryError(RetryOperationHealth, fmt.Errorf("wrapped: %w", test.err)); got.ErrorClass != test.class || got.Successful {
				t.Fatalf("classification=%#v", got)
			}
		})
	}
}

// TestClassifyRetryErrorControlFlow 验证 CAS、shutdown 和删除 not-found 的特殊控制流。
func TestClassifyRetryErrorControlFlow(t *testing.T) {
	if got := ClassifyRetryError(RetryOperationCreate, domain.ErrConflict); got.ErrorClass != RetryErrorConflict || got.Successful {
		t.Fatalf("CAS conflict=%#v", got)
	}
	if got := ClassifyRetryError(RetryOperationCreate, context.Canceled); got.ErrorClass != RetryErrorShutdown || got.Successful {
		t.Fatalf("shutdown=%#v", got)
	}
	for _, operation := range []RetryOperation{RetryOperationDelete, RetryOperationExpire, RetryOperationCleanup} {
		got := ClassifyRetryError(operation, domain.ErrNotFound)
		if !got.Successful || got.ErrorClass != RetryErrorAlreadyAbsent {
			t.Fatalf("operation=%s classification=%#v", operation, got)
		}
	}
	if got := ClassifyRetryError(RetryOperationStart, domain.ErrNotFound); got.Successful || got.ErrorClass != RetryErrorTransient {
		t.Fatalf("start not-found=%#v", got)
	}
	if got := ClassifyRetryError(RetryOperationCreate, nil); !got.Successful || got.ErrorClass != "" {
		t.Fatalf("nil=%#v", got)
	}
}

// TestClassifyRetryErrorUnknownAndStringSpoof 验证未知错误保守重试且相同文本不能伪装 typed error。
func TestClassifyRetryErrorUnknownAndStringSpoof(t *testing.T) {
	for _, err := range []error{
		errors.New("unknown failure"),
		errors.New((&runnerclient.ProtocolMismatchError{}).Error()),
		errors.New((&dockerruntime.SpecDriftError{}).Error()),
	} {
		if got := ClassifyRetryError(RetryOperationCleanup, err); got.ErrorClass != RetryErrorTransient || got.Successful || got.Reason != "" {
			t.Fatalf("spoof=%q classification=%#v", err, got)
		}
	}
}
