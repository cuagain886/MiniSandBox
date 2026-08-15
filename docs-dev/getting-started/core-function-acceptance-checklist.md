# MiniSandbox 核心功能验收清单（Go SDK 视角）

## 1. 验收定位

MiniSandbox 面向业务调用方的主要入口是 Go SDK，而不是 `curl`、`jq` 或一组 Bash 请求。

Bash 在验收中只负责两件事：启动 `sandboxd`，以及运行 SDK 验收程序。生命周期、续期、命令执行、日志读取、取消和删除都应通过 `minisandbox/sdk/go` 完成。

本文把验收分成两层：

1. **SDK 用户验收**：判断项目是否已经能够被 Go 程序正常使用，是本文的核心。
2. **运行时工程验收**：验证容器隔离、崩溃恢复、nftables 和内部 CIDR 阻断等实现不变量，由仓库测试负责，不要求 SDK 用户手工复现。

## 2. 当前 SDK 能力边界

| 能力 | SDK 方法 | 本次是否验收 |
|---|---|---|
| 创建 sandbox | `CreateSandboxWithOptions` | 是 |
| 幂等创建 | `CreateSandboxOptions.IdempotencyKey` | 是 |
| 查询生命周期状态 | `GetSandbox` | 是 |
| 续期 | `RenewSandbox` | 是 |
| 创建后台 execution | `StartBackgroundExecution` | 是 |
| 查询 execution | `GetExecution` | 是 |
| 游标读取 stdout/stderr 事件 | `GetExecutionLogs` | 是 |
| 取消 execution | `CancelExecution` | 是 |
| 删除 sandbox | `DeleteSandbox` | 是 |
| 前台 SSE 执行 | 暂无高层 SDK 方法 | 不作为 SDK 验收项 |
| readiness、metrics、admin | 暂无 SDK 方法 | 由服务运维验收覆盖 |

因此，当前 SDK 已经覆盖普通调用方的完整后台执行闭环，但还不是覆盖全部 HTTP API 的完整客户端。

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

### S01：创建并等待 Running

调用：

```go
ttl := 10 * time.Minute
client := sdk.NewClient("http://127.0.0.1:8080", nil)

sandbox, err := client.CreateSandboxWithOptions(ctx, sdk.CreateSandboxRequest{
    Image: "debian:bookworm-slim",
    TTL:   &ttl,
}, sdk.CreateSandboxOptions{
    IdempotencyKey: "sdk-acceptance-001",
})
```

随后每 200 毫秒调用一次 `GetSandbox`，最长等待 60 秒。

通过条件：

- 创建返回非空 `sandbox.ID`；
- 状态最终进入 `protocol.SandboxStateRunning`；
- `ExpiresAt` 晚于当前时间；
- 若进入 `Failed`，立即判定失败，并记录 `Reason` 和 `Message`。

### S02：验证幂等创建

使用完全相同的请求和 `IdempotencyKey` 再次调用 `CreateSandboxWithOptions`。

通过条件：第二次返回的 sandbox ID 与第一次相同，没有创建第二个 sandbox。

再用同一个 key、不同 TTL 或镜像调用一次。

通过条件：返回 `*sdk.ResponseError`，HTTP 状态为 `409`。

### S03：执行命令并读取日志

```go
execution, err := client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
    Argv: []string{
        "/bin/sh", "-c",
        "printf 'sdk-stdout'; printf 'sdk-stderr' >&2",
    },
    Timeout: 10 * time.Second,
})
```

随后调用 `GetExecution` 轮询终态，并从游标 `0` 开始调用 `GetExecutionLogs`。每次把游标更新为 `page.NextCursor`，直到 `page.Complete == true`。

`stdout` 和 `stderr` 内容位于事件的 `DataBase64` 字段，需要使用 `base64.StdEncoding.DecodeString` 解码。

通过条件：

- execution ID 非空；
- 状态最终为 `protocol.ExecutionStateExited`；
- 终止事件类型为 `protocol.EventExited`，退出码为 `0`；
- stdout 精确包含 `sdk-stdout`；
- stderr 精确包含 `sdk-stderr`；
- 所有事件的 `Sequence` 严格递增；
- 日志最终返回 `Complete == true`。

### S04：取消长任务

```go
execution, err := client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
    Argv:    []string{"/bin/sh", "-c", "sleep 30 & wait"},
    Timeout: 60 * time.Second,
})

err = client.CancelExecution(ctx, sandbox.ID, execution.ExecutionID)
```

通过条件：

- `CancelExecution` 成功；
- execution 最终进入 `protocol.ExecutionStateCancelled`；
- 终止事件类型为 `protocol.EventCancelled`；
- 再次取消不会破坏已确定的终态。

该项从 SDK 可见行为上验证“取消整个执行”的语义；是否残留子进程由运行时安全测试进一步验证。

### S05：续期

```go
renewed, err := client.RenewSandbox(ctx, sandbox.ID, sdk.RenewSandboxRequest{
    ExpiresAt: sandbox.ExpiresAt.Add(5 * time.Minute),
})
```

通过条件：

- 返回的新 `ExpiresAt` 等于请求值；
- 使用同一个 `ExpiresAt` 重试仍成功，且时间不再变化；
- 尝试缩短租约时返回 `*sdk.ResponseError`，HTTP 状态为 `409`。

### S06：错误模型与 Context

通过条件：

- 查询不存在的 sandbox 返回 `*sdk.ResponseError`，并保留 HTTP 状态和稳定错误详情；
- 使用已经取消的 `context.Context` 调用 SDK，返回 context 相关错误；
- 本地传入非法 TTL、非整秒 timeout 或非法幂等 key 时，SDK 在发请求前直接报错。

### S07：删除与重复删除

```go
err = client.DeleteSandbox(ctx, sandbox.ID)
```

随后轮询 `GetSandbox`，等待状态进入 `protocol.SandboxStateTerminated`。

通过条件：

- 首次删除成功；
- 状态最终为 `Terminated`；
- 对同一个 ID 再次调用 `DeleteSandbox` 仍成功；
- 删除后不能再创建新的 execution。

## 5. 执行完整验收程序

仓库已经提供只依赖公开 Go SDK 的完整验收程序：[go_sdk.go](../../tests/sdk/go_sdk.go)。在 `sandboxd` 就绪后，从仓库根目录执行：

```bash
go run ./tests/sdk
```

默认连接 `http://127.0.0.1:8080` 并使用 `debian:bookworm-slim`。如需覆盖：

```bash
MINISANDBOX_URL=http://127.0.0.1:18080 \
MINISANDBOX_IMAGE=debian:bookworm-slim \
go run ./tests/sdk
```

程序逐项打印 S01～S07；全部通过时打印 `7/7 PASS` 并以退出码 0 结束，任一失败时打印所属步骤并以非零退出码结束。程序会使用每次唯一的幂等 key，并在失败路径尽力删除本次创建的 sandbox。

## 6. SDK 验收通过标准

| 编号 | 验收项 | 结果 |
|---|---|---|
| S01 | 创建并进入 Running | ☐ PASS / ☐ FAIL |
| S02 | 幂等重放与冲突 | ☐ PASS / ☐ FAIL |
| S03 | 执行、状态、stdout/stderr 和日志游标 | ☐ PASS / ☐ FAIL |
| S04 | 取消长任务 | ☐ PASS / ☐ FAIL |
| S05 | 续期、重放和拒绝缩短 | ☐ PASS / ☐ FAIL |
| S06 | SDK 错误模型与 Context | ☐ PASS / ☐ FAIL |
| S07 | 删除、终态和重复删除 | ☐ PASS / ☐ FAIL |

全部 7 项通过，才能判定“面向 Go SDK 用户的核心功能可用”。

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

项目的设计初衷确实是让 Go 调用方通过 SDK 使用 MiniSandbox。当前最合适的验收主线是：

```text
SDK 创建 sandbox
  -> 等待 Running
  -> 启动后台 execution
  -> 查询状态和读取日志
  -> 取消或等待退出
  -> 续期
  -> 删除 sandbox
```

原先逐条调用 HTTP API 的 Bash 清单更适合协议调试或服务端排障，不应作为核心用户验收流程。
