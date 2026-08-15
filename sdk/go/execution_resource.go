package sdk

import (
	"context"
	"time"
)

// StartExecution 在当前 sandbox 中创建后台 execution 并返回资源对象。
//
// 本方法复用 StartBackgroundExecution 的请求校验和 wire 映射；调用方需要
// 前台流式执行时应使用 ExecuteStream。
func (s *Sandbox) StartExecution(
	ctx context.Context,
	request ExecuteRequest,
) (*Execution, error) {
	descriptor, err := s.client.StartBackgroundExecution(ctx, s.id, request)
	if err != nil {
		return nil, err
	}
	return &Execution{sandbox: s, id: descriptor.ExecutionID}, nil
}

// Execution 表示一个 sandbox 中的指定后台 execution 资源。
//
// 资源对象只保存所属 Sandbox 引用和稳定 execution ID，本身不缓存状态；
// 所有方法都在调用时向服务端查询或提交意图。
type Execution struct {
	sandbox *Sandbox
	id      string
}

// ID 返回该资源对象绑定的稳定 execution 标识。
func (e *Execution) ID() string {
	return e.id
}

// Info 查询当前 execution 状态并转换为 SDK 原生信息模型。
func (e *Execution) Info(ctx context.Context) (ExecutionInfo, error) {
	status, err := e.sandbox.client.GetExecution(ctx, e.sandbox.id, e.id)
	if err != nil {
		return ExecutionInfo{}, err
	}
	return newExecutionInfo(status)
}

// Wait 轮询 execution 状态直到进入任一合法终态并返回该时刻的信息。
//
// 终态为 Exited、Failed、Cancelled 和 TimedOut；非零退出码也属于 Exited，
// 本方法不把非零退出当作错误。总时长由调用方的 context deadline 控制。
func (e *Execution) Wait(ctx context.Context) (ExecutionInfo, error) {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()
	for {
		info, err := e.Info(ctx)
		if err != nil {
			return ExecutionInfo{}, err
		}
		if executionTerminalState(info.State) {
			return info, nil
		}
		select {
		case <-ctx.Done():
			return ExecutionInfo{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelAndWait 请求取消当前 execution 并等待其收敛到终态。
//
// 取消本身是异步意图；对已终态 execution 的重复取消由服务端按幂等语义
// 处理，因此本方法可以安全重试。返回的终态通常是 Cancelled，但如果进程
// 在取消生效前自然退出，也可能是 Exited 或 TimedOut。
func (e *Execution) CancelAndWait(ctx context.Context) (ExecutionInfo, error) {
	if err := e.sandbox.client.CancelExecution(ctx, e.sandbox.id, e.id); err != nil {
		return ExecutionInfo{}, err
	}
	return e.Wait(ctx)
}
