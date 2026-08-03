package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"minisandbox/pkg/protocol"
)

// ExecuteRequest 是 Go SDK 面向调用方的 execution 请求。
//
// Argv 与 Shell 必须且只能设置一个。Timeout 使用 Go 原生 time.Duration；SDK
// 只接受整秒值并映射为 wire timeout_seconds，避免隐式截断改变 deadline。
type ExecuteRequest struct {
	// Argv 是不经 shell 解析的参数数组，与 Shell 必须二选一。
	Argv []string
	// Shell 是显式请求 shell 解释的命令文本，与 Argv 必须二选一。
	Shell string
	// Cwd 是容器内执行目录；空字符串表示服务端默认 /workspace。
	Cwd string
	// Env 是本次命令附加的环境变量。
	Env map[string]string
	// Timeout 是执行超时；零表示使用服务端默认值。
	Timeout time.Duration
}

// wire 把 SDK 原生 duration 映射为稳定的秒级协议字段。
func (r ExecuteRequest) wire(background bool) (protocol.ExecuteRequest, error) {
	if r.Timeout < 0 || r.Timeout%time.Second != 0 {
		return protocol.ExecuteRequest{}, fmt.Errorf(
			"minisandbox: execution timeout must be a non-negative whole number of seconds",
		)
	}
	return protocol.ExecuteRequest{
		Argv:           r.Argv,
		Shell:          r.Shell,
		Cwd:            r.Cwd,
		Env:            r.Env,
		TimeoutSeconds: int64(r.Timeout / time.Second),
		Background:     background,
	}, nil
}

// StartBackgroundExecution 创建后台 execution 并返回其稳定描述符。
func (c *Client) StartBackgroundExecution(
	ctx context.Context,
	sandboxID string,
	request ExecuteRequest,
) (protocol.ExecutionDescriptor, error) {
	wire, err := request.wire(true)
	if err != nil {
		return protocol.ExecutionDescriptor{}, err
	}
	var descriptor protocol.ExecutionDescriptor
	err = c.doJSON(
		ctx,
		http.MethodPost,
		executionCollectionPath(sandboxID),
		wire,
		&descriptor,
	)
	return descriptor, err
}

// GetExecution 返回指定 sandbox 中后台 execution 的当前状态。
func (c *Client) GetExecution(
	ctx context.Context,
	sandboxID string,
	executionID string,
) (protocol.ExecutionStatus, error) {
	var status protocol.ExecutionStatus
	err := c.doJSON(
		ctx,
		http.MethodGet,
		executionResourcePath(sandboxID, executionID),
		nil,
		&status,
	)
	return status, err
}

// CancelExecution 请求取消运行中的 execution；终态 execution 的 204 也视为成功。
func (c *Client) CancelExecution(
	ctx context.Context,
	sandboxID string,
	executionID string,
) error {
	return c.doJSON(
		ctx,
		http.MethodDelete,
		executionResourcePath(sandboxID, executionID),
		nil,
		nil,
	)
}

// GetExecutionLogs 从最后已读 sequence 之后读取一页后台 execution 事件。
func (c *Client) GetExecutionLogs(
	ctx context.Context,
	sandboxID string,
	executionID string,
	cursor uint64,
) (protocol.ExecutionLogPage, error) {
	var page protocol.ExecutionLogPage
	path := executionResourcePath(sandboxID, executionID) +
		"/logs?cursor=" + strconv.FormatUint(cursor, 10)
	err := c.doJSON(ctx, http.MethodGet, path, nil, &page)
	return page, err
}

func executionCollectionPath(sandboxID string) string {
	return "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/executions"
}

func executionResourcePath(sandboxID, executionID string) string {
	return executionCollectionPath(sandboxID) + "/" + url.PathEscape(executionID)
}
