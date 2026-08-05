package runner

import (
	"errors"
	"strings"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

const maxExecutionArguments = 4096

// ErrInvalidExecutionRequest 是基础 execution 请求违反命令或 timeout 约束时的稳定内部错误。
var ErrInvalidExecutionRequest = errors.New("invalid execution request")

// ValidatedRequest 是基础校验后的命令与生命周期参数，不包含尚未校验的 env 和 cwd。
type ValidatedRequest struct {
	// Argv 是绕过 shell 直接执行的参数副本，与 Shell 必须二选一。
	Argv []string
	// Shell 是交给固定 shell 解释器的原始命令，与 Argv 必须二选一。
	Shell string
	// Timeout 是已经应用服务端默认值且不超过上限的执行时长。
	Timeout time.Duration
	// Background 只决定请求断开后的生命周期，不改变命令内容。
	Background bool
}

// RequestValidator 使用 runner bootstrap 中不可由请求扩大的 limit 校验基础执行参数。
type RequestValidator struct {
	defaultTimeout  time.Duration
	maxTimeout      time.Duration
	maxCommandBytes int64
}

// NewRequestValidator 从可信 bootstrap limits 创建 validator。
func NewRequestValidator(limits runnerbootstrap.Limits) (*RequestValidator, error) {
	if limits.DefaultTimeoutNanoseconds <= 0 ||
		limits.MaxTimeoutNanoseconds < limits.DefaultTimeoutNanoseconds ||
		limits.MaxRequestBytes <= 0 {
		return nil, errors.New("runner request limits are invalid")
	}
	return &RequestValidator{
		defaultTimeout:  limits.DefaultTimeoutNanoseconds,
		maxTimeout:      limits.MaxTimeoutNanoseconds,
		maxCommandBytes: limits.MaxRequestBytes,
	}, nil
}

// Validate 校验 argv/shell 互斥、命令有界性和 timeout，并返回与调用方输入解耦的新值。
// env 与 cwd 留给各自的安全边界校验器，本方法不会读取或修改它们。
func (v *RequestValidator) Validate(request protocol.ExecuteRequest) (ValidatedRequest, error) {
	if v == nil {
		return ValidatedRequest{}, ErrInvalidExecutionRequest
	}
	hasArgv := len(request.Argv) > 0
	hasShell := request.Shell != ""
	if hasArgv == hasShell {
		return ValidatedRequest{}, ErrInvalidExecutionRequest
	}
	validated := ValidatedRequest{Background: request.Background}
	if hasArgv {
		argv, err := v.validateArgv(request.Argv)
		if err != nil {
			return ValidatedRequest{}, err
		}
		validated.Argv = argv
	} else {
		if strings.TrimSpace(request.Shell) == "" ||
			strings.IndexByte(request.Shell, 0) >= 0 ||
			int64(len(request.Shell)) > v.maxCommandBytes {
			return ValidatedRequest{}, ErrInvalidExecutionRequest
		}
		validated.Shell = request.Shell
	}
	timeout, err := v.validateTimeout(request.TimeoutSeconds)
	if err != nil {
		return ValidatedRequest{}, err
	}
	validated.Timeout = timeout
	return validated, nil
}

func (v *RequestValidator) validateArgv(argv []string) ([]string, error) {
	if len(argv) == 0 || len(argv) > maxExecutionArguments || argv[0] == "" {
		return nil, ErrInvalidExecutionRequest
	}
	var totalBytes int64
	result := make([]string, len(argv))
	for index, argument := range argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return nil, ErrInvalidExecutionRequest
		}
		argumentBytes := int64(len(argument))
		if argumentBytes > v.maxCommandBytes-totalBytes {
			return nil, ErrInvalidExecutionRequest
		}
		totalBytes += argumentBytes
		result[index] = argument
	}
	return result, nil
}

func (v *RequestValidator) validateTimeout(seconds int64) (time.Duration, error) {
	if seconds < 0 {
		return 0, ErrInvalidExecutionRequest
	}
	if seconds == 0 {
		return v.defaultTimeout, nil
	}
	if seconds > int64(v.maxTimeout/time.Second) || seconds > int64((time.Duration(1<<63-1))/time.Second) {
		return 0, ErrInvalidExecutionRequest
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout <= 0 || timeout > v.maxTimeout {
		return 0, ErrInvalidExecutionRequest
	}
	return timeout, nil
}
