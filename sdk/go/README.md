# MiniSandbox Go SDK

面向 MiniSandbox 生命周期 API 的 Go 客户端。高层接口用 `Client → Sandbox → Execution`
三级资源对象覆盖核心工作流；需要精确控制请求过程时可退回保留的底层方法。

```go
client := sdk.NewClient("http://127.0.0.1:8080", nil)

sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{
    Image: "debian:bookworm-slim",
})
if err != nil {
    return err
}
defer sandbox.Delete(context.Background())

if _, err := sandbox.WaitRunning(ctx); err != nil {
    return err
}

result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
    Argv: []string{"/bin/sh", "-c", "echo hello"},
})
if err != nil {
    return err
}
fmt.Print(string(result.Stdout))
```

## 前置条件

- 已启动的 `sandboxd`（见根 README 的 Build & Run）；
- Go module 中引入本仓库（SDK 与服务端共用根 module `minisandbox`，导入路径为
  `minisandbox/sdk/go`）；
- `NewClient(baseURL, httpClient)` 的 `httpClient` 可为 nil；需要自定义超时、
  transport 时传入自定义 `*http.Client`。

## Client

```go
client := sdk.NewClient(baseURL, httpClient)

err := client.Health(ctx)                          // /healthz 存活探测
readiness, err := client.Readiness(ctx)            // /readyz 组件状态；未就绪时 Ready=false 且 err=nil

sandbox, err := client.Create(ctx, request,
    sdk.WithIdempotencyKey("job-001"))            // 创建并返回 *Sandbox
sandbox = client.Sandbox(sandboxID)                // 用已知 ID 绑定资源对象
```

`WithIdempotencyKey` 让同一 key 的相同创建请求安全重放；同一 key 的不同请求返回
409。`Readiness` 把 503 视为正常观测结果，只有传输失败才是错误。

## Sandbox 生命周期

```go
info, err := sandbox.Info(ctx)                     // 当前状态
info, err = sandbox.WaitRunning(ctx)               // 轮询至 Running；Failed/Terminated 提前失败
info, err = sandbox.Renew(ctx, expiresAt)          // 延长租约到绝对时间

err = sandbox.Delete(ctx)                          // 提交删除意图
info, err = sandbox.DeleteAndWait(ctx)             // 删除并等待 Terminated
```

等待方法按内置间隔轮询，总时长由调用方的 context deadline 控制；SDK 不内置超时。

## 执行命令

### Run：一次调用完成执行

```go
result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
    Argv:    []string{"/bin/sh", "-c", "make test"},
    Env:     map[string]string{"CI": "true"},
    Timeout: 10 * time.Minute,
})
```

`RunResult` 携带 `ExecutionID`、`State`、`ExitCode`、`Stdout`、`Stderr`、
`Duration` 和 `OutputTruncated`。退出码为零时 err 为 nil；非零退出返回
`*sdk.ExitError`，取消、超时和失败分别返回 `*sdk.ExecutionCancelledError`、
`*sdk.ExecutionTimedOutError` 和 `*sdk.ExecutionFailedError`。所有终态错误都
同时返回已收集的 `RunResult`。

### ExecuteStream：前台 SSE 流式执行

```go
stream, err := sandbox.ExecuteStream(ctx, sdk.ExecuteRequest{
    Argv: []string{"/bin/sh", "-c", "ping -c 3 127.0.0.1"},
})
if err != nil {
    return err
}
for stream.Next() {
    event := stream.Event()
    if event.Type == sdk.EventStdout {
        os.Stdout.Write(event.Data)
    }
}
if err := stream.Err(); err != nil {
    return err
}
```

事件按到达顺序解码交付，流以唯一终止事件结束。提前放弃时调用 `stream.Close()`，
服务端会按取消语义终止整个进程组。

### 后台执行与日志

```go
execution, err := sandbox.StartExecution(ctx, request)  // 启动后台任务

info, err := execution.Info(ctx)                    // 当前状态
info, err = execution.Wait(ctx)                     // 等待任一终态
info, err = execution.CancelAndWait(ctx)            // 取消并等待收敛

logs := execution.Logs(ctx, 0)                      // 从头读取；也可从 sequence 续读
for logs.Next() {
    event := logs.Event()
    fmt.Printf("%s: %s", event.Type, event.Data)
}
if err := logs.Err(); err != nil {
    return err
}
```

日志迭代器内部维护 cursor、自动翻页并解码 Base64；日志追平终止事件后自然结束。

## 错误处理

- HTTP 层错误统一为 `*sdk.ResponseError`，可读取 `StatusCode`、`Detail.Code`、
  `Detail.RequestID` 和 `Detail.Retryable`，并提供 `IsNotFound`、`IsConflict`
  和 `IsRetryable` 判断；
- Run 的终态错误见上节，均可用 `errors.As` 区分；
- 非法参数（TTL 越界、非整秒 timeout、非法幂等 key 等）在发请求前直接报错。

## 底层方法

以下方法保持可用，适合需要精确控制请求过程的调用方；高层接口均在它们之上实现：

`CreateSandbox`、`CreateSandboxWithOptions`、`GetSandbox`、`RenewSandbox`、
`DeleteSandbox`、`StartBackgroundExecution`、`GetExecution`、`GetExecutionLogs`、
`CancelExecution`。

## 验收

SDK 面向调用方的完整闭环由仓库验收程序覆盖；`sandboxd` 就绪后执行：

```bash
go run ./tests/sdk
```

10 项（创建、幂等、后台执行日志、取消、续期、Run、SSE、错误模型、删除、
health/readiness）全部通过时打印 `10/10 PASS`。
