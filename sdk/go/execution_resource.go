package sdk

import "context"

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
