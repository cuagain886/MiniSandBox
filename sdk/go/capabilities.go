package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"minisandbox/pkg/protocol"
)

// Capabilities 是当前 sandbox runner 实际提供的功能能力集合。
type Capabilities = protocol.Capabilities

// Capabilities 查询当前 sandbox 的功能能力。
//
// 仅 Running 状态的 sandbox 可查询；每个能力相互独立，调用方必须把
// 任一能力视为可选。
func (s *Sandbox) Capabilities(ctx context.Context) (Capabilities, error) {
	var capabilities Capabilities
	err := s.client.doJSON(
		ctx,
		http.MethodGet,
		"/v1/sandboxes/"+url.PathEscape(s.id)+"/capabilities",
		nil,
		&capabilities,
	)
	if err != nil {
		return Capabilities{}, err
	}
	return capabilities, nil
}

// WaitReady 等待 sandbox 进入 Running 并确认 runner 能力可用。
//
// 本方法等价于 WaitRunning 成功后再查询一次 capabilities；Failed 或
// 提前 Terminated 时沿用 WaitRunning 的错误语义。总时长由调用方 context
// deadline 控制。
func (s *Sandbox) WaitReady(ctx context.Context) (SandboxInfo, Capabilities, error) {
	info, err := s.WaitRunning(ctx)
	if err != nil {
		return SandboxInfo{}, Capabilities{}, err
	}
	// 短暂重试吸收 Running 与 capabilities 就绪之间的窗口。
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()
	for {
		capabilities, err := s.Capabilities(ctx)
		if err == nil {
			return info, capabilities, nil
		}
		var responseError *ResponseError
		if !errors.As(err, &responseError) || !responseError.IsRetryable() {
			return SandboxInfo{}, Capabilities{}, err
		}
		select {
		case <-ctx.Done():
			return SandboxInfo{}, Capabilities{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
