# MiniSandbox 核心功能验收清单（Go SDK 视角）

## 1. 验收定位

MiniSandbox 面向业务调用方的主要入口是 Go SDK，而不是 `curl`、`jq` 或一组 Bash 请求。

Bash 在验收中只负责两件事：启动 `sandboxd`，以及运行 SDK 验收程序。生命周期、续期、命令执行、日志读取、取消和删除都应通过 `minisandbox/sdk/go` 完成。

本文把验收分成两层：

1. **SDK 用户验收**：判断项目是否已经能够被 Go 程序正常使用，是本文的核心。
2. **运行时工程验收**：验证容器隔离、崩溃恢复、nftables 和内部 CIDR 阻断等实现不变量，由仓库测试负责，不要求 SDK 用户手工复现。

## 2. 当前 SDK 能力边界

SDK Phase 完成后，高层接口覆盖调用方完整工作流；需要精确控制请求过程时可退回底层方法。

| 能力 | SDK 高层方法 | 本次是否验收 |
|---|---|---|
| 服务存活 / 就绪 | `Client.Health`、`Client.Readiness` | 是 |
| 创建 sandbox | `Client.Create`（支持 `WithIdempotencyKey`） | 是 |
| 绑定已有 sandbox | `Client.Sandbox(id)` | 是 |
| 查询生命周期状态 | `Sandbox.Info` | 是 |
| 等待 Running | `Sandbox.WaitRunning` | 是 |
| 续期 | `Sandbox.Renew` | 是 |
| 删除 | `Sandbox.Delete`、`Sandbox.DeleteAndWait` | 是 |
| 一次调用执行命令 | `Sandbox.Run` | 是 |
| 前台 SSE 流式执行 | `Sandbox.ExecuteStream` | 是 |
| 启动后台 execution | `Sandbox.StartExecution` | 是 |
| 查询 / 等待 execution | `Execution.Info`、`Execution.Wait` | 是 |
| 读取日志 | `Execution.Logs`（自动翻页解码） | 是 |
| 取消 | `Execution.CancelAndWait` | 是 |
| 底层精确控制 | `CreateSandboxWithOptions`、`GetSandbox` 等 9 个方法 | 由 SDK 单测回归 |

## 3. 验收前提

- 在 Linux/amd64 或 WSL2 中运行；
- Docker Engine 可用；
- 已按根目录 README 启动 `sandboxd`；
- `http://127.0.0.1:8080/readyz` 已就绪；
- 默认验收镜像为 `debian:bookworm-slim`。

启动服务仍可按 README 执行：

```bash
make build
sudo ./bin/sandboxd --config configs/sandboxd.example.yaml
```

这不是业务调用方式，只是准备被 SDK 调用的服务端。

## 4. SDK 核心验收步骤

以下步骤由验收程序自动执行；此处列出每步的调用方式和通过条件。

### S10：Health 与 Readiness（环境预检）

```go
err := client.Health(ctx)
readiness, err := client.Readiness(ctx)
```

通过条件：Health 无错误；Readiness 返回的 `Ready` 为 true 且全部组件就绪。

### S01：创建并等待 Running

```go
sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{
    Image: "debian:bookworm-slim",
    TTL:   &ttl,
}, sdk.WithIdempotencyKey("sdk-acceptance-001"))

info, err := sandbox.WaitRunning(ctx)
```

通过条件：创建返回资源对象；`WaitRunning` 收敛到 Running；`ExpiresAt` 晚于当前时间；Failed/Terminated 提前失败并携带 reason。

### S02：验证幂等创建

使用完全相同的请求和 key 再次 `Create`。

通过条件：第二次返回的 sandbox ID 与第一次相同；同一 key、不同请求返回 409。

### S03：后台执行并读取日志

```go
execution, err := sandbox.StartExecution(ctx, sdk.ExecuteRequest{
    Argv:    []string{"/bin/sh", "-c", "printf 'sdk-stdout'; printf 'sdk-stderr' >&2"},
    Timeout: 10 * time.Second,
})
info, err := execution.Wait(ctx)

logs := execution.Logs(ctx, 0)
for logs.Next() { event := logs.Event() /* ... */ }
```

通过条件：终态为 Exited 且退出码为 0；日志迭代器自动翻页并解码，stdout/stderr 内容精确匹配。

### S04：取消长任务

```go
info, err := execution.CancelAndWait(ctx)
```

通过条件：终态收敛为 Cancelled 且终止事件为 `cancelled`；重复 CancelAndWait 不破坏终态。

### S05：续期

```go
info, err := sandbox.Renew(ctx, sandboxInfo.ExpiresAt.Add(5*time.Minute))
```

通过条件：返回的 `ExpiresAt` 等于请求值；同值重放仍成功；缩短租约返回 409。

### S08：Run 一次调用执行

```go
result, err := sandbox.Run(ctx, sdk.ExecuteRequest{ /* ... */ })
```

通过条件：正常命令返回 `ExitCode` 0 与精确的 stdout/stderr；`exit 7` 返回 `*sdk.ExitError`（退出码 7）且结果对象仍携带输出。

### S09：前台 SSE 流式执行

```go
stream, err := sandbox.ExecuteStream(ctx, sdk.ExecuteRequest{ /* ... */ })
for stream.Next() { event := stream.Event() /* ... */ }
```

通过条件：事件按序解码；stdout/stderr 精确匹配；流以 exit 0 的 `exited` 终止事件结束且 `Err()` 为 nil。

### S06：错误模型与 Context

通过条件：

- 查询不存在的 sandbox 返回 `*sdk.ResponseError`，并保留 HTTP 状态和稳定错误详情；
- 使用已经取消的 `context.Context` 调用 SDK，返回 context 相关错误；
- 本地传入非法 TTL、非整秒 timeout 或非法幂等 key 时，SDK 在发请求前直接报错。

### S07：删除与重复删除

```go
info, err := sandbox.DeleteAndWait(ctx)
```

通过条件：首次删除成功；状态收敛 Terminated；重复 `Delete` 仍成功；删除后创建 execution 返回 409。

## 5. 执行完整验收程序

仓库已经提供只依赖公开 Go SDK 高层接口的完整验收程序：[go_sdk.go](../../tests/sdk/go_sdk.go)。在 `sandboxd` 就绪后，从仓库根目录执行：

```bash
go run ./tests/sdk
```

默认连接 `http://127.0.0.1:8080` 并使用 `debian:bookworm-slim`。如需覆盖：

```bash
MINISANDBOX_URL=http://127.0.0.1:18080 \
MINISANDBOX_IMAGE=debian:bookworm-slim \
go run ./tests/sdk
```

程序逐项打印 S01～S10（S10 环境预检最先执行）；全部通过时打印 `10/10 PASS` 并以退出码 0 结束，任一失败时打印所属步骤并以非零退出码结束。程序会使用每次唯一的幂等 key，并在失败路径尽力删除本次创建的 sandbox。

## 6. SDK 验收通过标准

| 编号 | 验收项 | 结果 |
|---|---|---|
| S10 | Health 与 Readiness 预检 | ☐ PASS / ☐ FAIL |
| S01 | 创建并进入 Running | ☐ PASS / ☐ FAIL |
| S02 | 幂等重放与冲突 | ☐ PASS / ☐ FAIL |
| S03 | 后台执行、状态与自动解码日志 | ☐ PASS / ☐ FAIL |
| S04 | 取消长任务 | ☐ PASS / ☐ FAIL |
| S05 | 续期、重放和拒绝缩短 | ☐ PASS / ☐ FAIL |
| S08 | Run 一次调用执行与非零退出 | ☐ PASS / ☐ FAIL |
| S09 | 前台 SSE 流式执行 | ☐ PASS / ☐ FAIL |
| S06 | SDK 错误模型与 Context | ☐ PASS / ☐ FAIL |
| S07 | 删除、终态和重复删除 | ☐ PASS / ☐ FAIL |

全部 10 项通过，才能判定“面向 Go SDK 用户的核心功能可用”。

## 7. SDK 之外仍需保留的工程验收

以下能力不能只看 SDK 返回值，仍应由自动化 integration/security tests 验证：

- 非 root runner 与进程组无残留；
- `sandbox-init` 的 PID 1、信号转发和孤儿回收；
- sandbox 独立 egress 网络命名空间；
- nftables 无条件屏蔽内部 CIDR；
- Docker、SQLite 或 `sandboxd` 重启后的 reconcile 恢复；
- trusted orphan、anomaly、TTL cleanup 和资源泄漏；
- admin token、metrics 以及默认关闭语义。

这两类验收并不冲突：SDK 验收回答“调用方能不能用”，工程验收回答“底层隔离和恢复是否可信”。用户日常使用应以前者为入口。

## 8. 当前结论

SDK 的推荐使用主线是：

```text
Client.Create
  -> Sandbox.WaitRunning
  -> Sandbox.Run（或 StartExecution + Wait + Logs / ExecuteStream）
  -> Sandbox.Renew（可选）
  -> Sandbox.DeleteAndWait
```

原先逐条调用 HTTP API 的 Bash 清单更适合协议调试或服务端排障，不应作为核心用户验收流程。
