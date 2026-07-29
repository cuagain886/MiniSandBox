package runnerclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"minisandbox/pkg/protocol"
)

const runnerSocketName = "runner.sock"

// SocketMissingError 表示当前 sandbox 的 runner Unix Socket 尚不存在。
type SocketMissingError struct {
	cause error
}

// Error 返回不包含宿主机 socket 路径的固定文案。
func (*SocketMissingError) Error() string {
	return "runner socket is missing"
}

// Unwrap 返回底层文件系统错误，供 errors.Is 分类。
func (e *SocketMissingError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 runner unhealthy 生命周期 reason。
func (*SocketMissingError) FailureReason() string {
	return string(protocol.SandboxReasonRunnerUnhealthy)
}

// UnhealthyError 表示 runner 返回非 200 状态或连接出现非缺失故障。
type UnhealthyError struct {
	statusCode int
	cause      error
}

// Error 返回不包含响应正文、token 或 socket 路径的固定文案。
func (*UnhealthyError) Error() string {
	return "runner is unhealthy"
}

// Unwrap 返回内部 HTTP 或 transport cause。
func (e *UnhealthyError) Unwrap() error {
	return e.cause
}

// StatusCode 返回 runner HTTP 状态；连接故障时为 0。
func (e *UnhealthyError) StatusCode() int {
	return e.statusCode
}

// FailureReason 返回稳定的 runner unhealthy 生命周期 reason。
func (*UnhealthyError) FailureReason() string {
	return string(protocol.SandboxReasonRunnerUnhealthy)
}

// TimeoutError 表示 runner 未在配置的 ready timeout 内响应。
type TimeoutError struct {
	cause error
}

// Error 返回不包含内部地址的固定超时文案。
func (*TimeoutError) Error() string {
	return "runner probe timed out"
}

// Unwrap 返回 context deadline cause。
func (e *TimeoutError) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 runner unhealthy 生命周期 reason。
func (*TimeoutError) FailureReason() string {
	return string(protocol.SandboxReasonRunnerUnhealthy)
}

// Probe 按 sandbox ID 对固定 Unix Socket `/healthz` 执行健康检查。
type Probe struct {
	socketRoot string
	timeout    time.Duration
	token      string
}

// NewRunnerProbe 创建绑定到受管 socket 根目录的健康检查 adapter。
//
// socketRoot 必须是绝对路径；timeout 必须为正数。token 只保存在内存中，
// 不进入 URL、错误文本或日志。
func NewRunnerProbe(
	socketRoot string,
	timeout time.Duration,
	token string,
) (*Probe, error) {
	if !filepath.IsAbs(socketRoot) {
		return nil, errors.New("runner socket root must be absolute")
	}
	if timeout <= 0 {
		return nil, errors.New("runner ready timeout must be positive")
	}
	return &Probe{
		socketRoot: filepath.Clean(socketRoot),
		timeout:    timeout,
		token:      token,
	}, nil
}

// Probe 只从规范 sandbox ID 推导 socket path 并请求固定 `/healthz`。
func (p *Probe) Probe(ctx context.Context, sandboxID string) error {
	socketPath, err := p.socketPath(sandboxID)
	if err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	client := New(socketPath, p.token)
	// Probe 的唯一时间边界必须来自 runner ready timeout；通用 Client 的
	// 30 秒默认值会在配置更大时提前终止并被误分类为 unhealthy。
	client.httpClient.Timeout = 0
	err = client.Health(operationContext)
	if err == nil {
		return nil
	}
	if errors.Is(operationContext.Err(), context.DeadlineExceeded) {
		return &TimeoutError{cause: context.DeadlineExceeded}
	}
	if errors.Is(operationContext.Err(), context.Canceled) {
		return context.Canceled
	}
	var statusError *StatusError
	if errors.As(err, &statusError) {
		return &UnhealthyError{
			statusCode: statusError.StatusCode,
			cause:      err,
		}
	}
	if isSocketMissing(err) {
		return &SocketMissingError{cause: err}
	}
	return &UnhealthyError{cause: err}
}

// socketPath 校验 UUID v4 并证明结果仍是 socketRoot 的直接子路径。
func (p *Probe) socketPath(sandboxID string) (string, error) {
	if p == nil || !validSandboxID(sandboxID) {
		return "", fmt.Errorf("sandbox ID is invalid")
	}
	directory := filepath.Join(p.socketRoot, sandboxID)
	relative, err := filepath.Rel(p.socketRoot, directory)
	if err != nil ||
		relative != sandboxID ||
		filepath.IsAbs(relative) ||
		strings.Contains(relative, string(filepath.Separator)) {
		return "", errors.New("runner socket path escapes managed root")
	}
	return filepath.Join(directory, runnerSocketName), nil
}

// isSocketMissing 识别 Unix dial 返回的 ENOENT，不依赖错误文本。
func isSocketMissing(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var operationError *net.OpError
	return errors.As(err, &operationError) &&
		errors.Is(operationError.Err, os.ErrNotExist)
}

// validSandboxID 只接受规范小写 UUID v4，阻止分隔符和路径穿越。
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
		value := id[index]
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}
