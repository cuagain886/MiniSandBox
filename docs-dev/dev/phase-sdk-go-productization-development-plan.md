# SDK Phase：Go SDK 易用化开发计划

> - 状态：待执行，`SDK-000` 尚未开始
> - 当前基线：Phase 3 已完成，现有 SDK 已通过基础 `7/7 PASS` 验收
> - 后续阶段：[Phase 4：Agent 体验开发计划](./phase-4-agent-experience-development-plan.md)
> - 当前验收：[Go SDK 核心功能验收清单](../getting-started/core-function-acceptance-checklist.md)

## 1. 目标

当前 `sdk/go` 已经可以创建 sandbox、执行命令和删除 sandbox，但调用方仍需要自己写状态轮询、日志游标、Base64 解码和终态判断。

本阶段只解决一个问题：让 Go 用户可以简单、完整地使用 MiniSandbox 已经具备的功能。

完成后，推荐用法应接近：

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

## 2. 开发原则

本阶段采用以下简化原则：

1. 继续使用当前 `sdk/go` 目录和根 Go module；
2. 在现有 SDK 上增加易用接口，不进行独立 module 拆分；
3. 优先复用已经存在的生命周期和 execution 方法；
4. 不增加服务端 API，也不修改现有协议语义；
5. 不实现复杂自动重试、观测 hook、API snapshot 或发布流水线；
6. 不为尚不存在的使用场景提前设计抽象；
7. 功能任务只描述实现目标，不为每个小任务单独设计一套测试；
8. 测试集中在每个阶段结束的验收任务中执行；
9. 每个任务仍按仓库规范独立提交，提交前只做必要的格式化和已有受影响包检查；
10. Phase 4 的 files、PTY、port proxy 等能力以后沿用本阶段形成的 SDK 使用方式。

## 3. 本阶段实现范围

### 3.1 保留现有底层操作

继续保留：

- `CreateSandboxWithOptions`；
- `GetSandbox`；
- `RenewSandbox`；
- `DeleteSandbox`；
- `StartBackgroundExecution`；
- `GetExecution`；
- `GetExecutionLogs`；
- `CancelExecution`。

这些方法仍供需要精确控制请求过程的调用方使用。

### 3.2 增加易用接口

本阶段新增：

- SDK 自己可直接使用的状态、事件和结果类型；
- `Sandbox` 资源对象；
- `Execution` 资源对象；
- `Client.Create`；
- `Sandbox.WaitRunning`；
- `Sandbox.Renew`；
- `Sandbox.DeleteAndWait`；
- `Sandbox.StartExecution`；
- `Execution.Wait`；
- `Execution.CancelAndWait`；
- 自动解码的 execution 日志读取；
- `Sandbox.Run`；
- 前台 SSE 流式执行；
- health 和 readiness 查询；
- 简洁的 README、示例和完整验收程序。

### 3.3 本阶段不做

- SDK 独立仓库或独立 Go module；
- Git tag、Go Proxy 或公开发布；
- TypeScript、Python 或浏览器 SDK；
- 自动启动 `sandboxd` 或 Docker；
- files、目录、PTY、port proxy、capabilities；
- 自动重试所有失败请求；
- 自动续租守护进程；
- 自动删除所有失败 sandbox；
- metrics、tracing 或第三方可观测性 SDK；
- 复杂插件体系、middleware 链或代码生成；
- 尚未出现在当前 OpenAPI 中的假设功能。

## 4. 目标 API

### 4.1 Client

```go
client := sdk.NewClient(baseURL, httpClient)

health, err := client.Health(ctx)
readiness, err := client.Readiness(ctx)

sandbox, err := client.Create(ctx, request,
    sdk.WithIdempotencyKey("job-001"),
)

sandbox = client.Sandbox(sandboxID)
```

现有 `NewClient` 保持不变，不为本阶段引入新的构造器体系。

### 4.2 Sandbox

```go
info, err := sandbox.Info(ctx)
info, err = sandbox.WaitRunning(ctx)
info, err = sandbox.Renew(ctx, expiresAt)

execution, err := sandbox.StartExecution(ctx, request)
result, err := sandbox.Run(ctx, request)
stream, err := sandbox.ExecuteStream(ctx, request)

err = sandbox.Delete(ctx)
info, err = sandbox.DeleteAndWait(ctx)
```

### 4.3 Execution

```go
info, err := execution.Info(ctx)
info, err = execution.Wait(ctx)

logs := execution.Logs(ctx, 0)
for logs.Next() {
    event := logs.Event()
    fmt.Printf("%s: %s", event.Type, event.Data)
}
if err := logs.Err(); err != nil {
    return err
}

info, err = execution.CancelAndWait(ctx)
```

### 4.4 RunResult

```go
type RunResult struct {
    ExecutionID     string
    State           ExecutionState
    ExitCode        int
    Stdout          []byte
    Stderr          []byte
    Duration        time.Duration
    OutputTruncated bool
}
```

`Run` 的基本语义：

- 启动一个后台 execution；
- 等待 execution 结束；
- 读取完整日志；
- 自动区分 stdout 和 stderr；
- 正常退出返回 `RunResult`；
- 非零退出、取消、超时或执行失败返回结果和相应错误。

## 5. 阶段划分与测试时点

测试不再分散到每个功能任务中，而是在以下五个阶段时点统一完成：

| 验收任务 | 验收范围 |
|---|---|
| `SDK-005` | 公共类型和基础 Client |
| `SDK-011` | Sandbox 生命周期易用接口 |
| `SDK-018` | Execution、日志、Run 和 SSE |
| `SDK-023` | README、示例和真实服务端工作流 |
| `SDK-025` | 全阶段最终回归 |

普通功能任务不再单独新增或列举测试用例，提交前只运行仓库规范要求的已有受影响包检查。新增的成体系测试、阶段回归和真实运行统一放在上述验收任务完成。

## 6. 任务总览

| 阶段 | 任务 | 交付结果 |
|---|---:|---|
| A. SDK 基础接口 | SDK-000～SDK-005 | 易用 API 方案、SDK 类型和基础 Client |
| B. Sandbox 生命周期 | SDK-006～SDK-011 | Sandbox handle、等待、续期和删除 |
| C. Execution 与 Run | SDK-012～SDK-018 | Execution handle、日志、Run 和 SSE |
| D. 服务状态与使用文档 | SDK-019～SDK-023 | health/readiness、示例和真实验收 |
| E. 收尾 | SDK-024～SDK-025 | API 指南、最终回归和验收报告 |

## 7. 详细任务

### A. SDK 基础接口

### SDK-000：确认当前 SDK 基线

- **目标**：记录当前 SDK 已有方法、当前 `7/7 PASS` 结果和主要使用问题。
- **实现**：生成简短 kickoff checklist，确认当前代码能够继续作为改造基础。
- **依赖**：Phase 3 已完成，`sandboxd` 能正常启动。

### SDK-001：确定推荐使用流程

- **目标**：确定 Client → Sandbox → Execution 的推荐调用方式。
- **实现**：把第 4 节 API 草案整理成最终方法列表，统一方法名和返回类型。
- **依赖**：SDK-000。

### SDK-002：补齐 SDK 状态和事件类型

- **目标**：让用户只导入 `sdk/go` 就能使用 sandbox state、execution state 和 event type。
- **实现**：在 SDK 中公开必要类型和常量，避免普通示例额外导入 `pkg/protocol`。
- **依赖**：SDK-001。

### SDK-003：增加 SDK 信息和结果类型

- **目标**：增加 `SandboxInfo`、`ExecutionInfo`、`ExecutionEvent` 和 `RunResult`。
- **实现**：沿用当前协议字段和 Go 原生时间类型，提供内部转换函数。
- **依赖**：SDK-002。

### SDK-004：整理 SDK 错误使用方式

- **目标**：让调用方可以直接判断 HTTP status、错误 code 和 execution 终态错误。
- **实现**：保留 `ResponseError`，补充少量必要 helper，并定义 Run 使用的退出、取消和超时错误。
- **依赖**：SDK-003。

### SDK-005：验收 SDK 基础接口

- **目标**：集中验证 SDK-001～SDK-004。
- **验收内容**：公共类型可直接使用；API 示例可编译；现有底层方法保持可用；SDK package tests 和 `go vet` 通过。
- **依赖**：SDK-001～SDK-004。

### B. Sandbox 生命周期

### SDK-006：实现 Sandbox 资源对象

- **目标**：用一个对象表示指定 sandbox。
- **实现**：`Sandbox` 保存 client 和 sandbox ID，并提供 `ID()`。
- **依赖**：SDK-005。

### SDK-007：实现 Client.Create

- **目标**：创建 sandbox 后直接返回 `*Sandbox`。
- **实现**：复用 `CreateSandboxWithOptions`；支持 image、TTL、outbound 和幂等 key。
- **依赖**：SDK-006。

### SDK-008：实现 Sandbox.Info

- **目标**：通过资源对象查询当前 sandbox 信息。
- **实现**：复用 `GetSandbox` 并转换为 `SandboxInfo`。
- **依赖**：SDK-006。

### SDK-009：实现 Sandbox.WaitRunning

- **目标**：调用方不再手写状态轮询。
- **实现**：定期调用 `Info`，进入 Running 时返回；进入 Failed/Terminated 或 context 结束时返回错误。
- **依赖**：SDK-008。

### SDK-010：实现续期和删除便利方法

- **目标**：完成 sandbox 生命周期闭环。
- **实现**：增加 `Renew`、`Delete` 和 `DeleteAndWait`；复用现有 renew/delete 方法。
- **依赖**：SDK-008、SDK-009。

### SDK-011：验收 Sandbox 生命周期

- **目标**：集中验证 SDK-006～SDK-010。
- **验收内容**：create → WaitRunning → Info → Renew → DeleteAndWait 完整通过；底层 lifecycle API 回归通过。
- **依赖**：SDK-006～SDK-010。

### C. Execution、日志与 Run

### SDK-012：实现 Execution 资源对象

- **目标**：用一个对象表示 sandbox 中的指定 execution。
- **实现**：`Execution` 保存 sandbox、execution ID，并提供 `ID()`。
- **依赖**：SDK-011。

### SDK-013：实现 StartExecution 和 Execution.Info

- **目标**：通过 Sandbox 启动任务，通过 Execution 查询状态。
- **实现**：复用 `StartBackgroundExecution` 和 `GetExecution`。
- **依赖**：SDK-012。

### SDK-014：实现 Execution.Wait 和 CancelAndWait

- **目标**：调用方不再手写 execution 状态轮询和取消后的等待。
- **实现**：`Wait` 等待任一合法终态；`CancelAndWait` 先取消再调用 `Wait`。
- **依赖**：SDK-013。

### SDK-015：实现日志迭代器

- **目标**：调用方不再维护 cursor 或手动解码 Base64。
- **实现**：迭代调用 `GetExecutionLogs`，返回已解码的 stdout/stderr event，日志完成后结束。
- **依赖**：SDK-013、SDK-014。

### SDK-016：实现 Sandbox.Run

- **目标**：用一次方法调用完成普通命令执行。
- **实现**：组合 StartExecution、Wait 和日志读取，生成 `RunResult`。
- **依赖**：SDK-014、SDK-015。

### SDK-017：实现 ExecuteStream

- **目标**：支持边执行边读取前台 SSE 输出。
- **实现**：封装现有前台 execution SSE 接口，向用户返回事件迭代器。
- **依赖**：SDK-003、现有 SSE 协议。

### SDK-018：验收 Execution 与 Run

- **目标**：集中验证 SDK-012～SDK-017。
- **验收内容**：正常执行、stdout/stderr、非零退出、取消、超时、日志读取、`Run` 和前台 SSE 均通过；SDK execution 回归通过。
- **依赖**：SDK-012～SDK-017。

### D. 服务状态与使用文档

### SDK-019：实现 Health

- **目标**：通过 SDK 查询 `sandboxd` 是否存活。
- **实现**：增加 `Client.Health(ctx)`，映射 `/healthz`。
- **依赖**：SDK-005。

### SDK-020：实现 Readiness

- **目标**：通过 SDK 查询服务及依赖是否就绪。
- **实现**：增加 `Client.Readiness(ctx)`，返回当前 readiness 组件状态。
- **依赖**：SDK-019。

### SDK-021：编写最小 Quickstart

- **目标**：用户可以复制一段简短代码完成第一次命令执行。
- **实现**：示例只包含 NewClient、Create、WaitRunning、Run 和 Delete，不展示底层 HTTP 细节。
- **依赖**：SDK-016、SDK-019、SDK-020。

### SDK-022：改造完整 SDK 验收程序

- **目标**：让 `tests/sdk/go_sdk.go` 使用新的高层 SDK 接口。
- **实现**：保留现有 S01～S07，再加入 Health、Readiness、Run 和 SSE，删除手写轮询、cursor 与 Base64 解码。
- **依赖**：SDK-016～SDK-021。

### SDK-023：阶段性真实服务验收

- **目标**：在真实 Linux/WSL2 + Docker 环境统一验证当前 SDK。
- **验收内容**：运行 SDK package tests、根 module 回归以及 `go run ./tests/sdk`；所有高层工作流通过，sandbox 最终完成清理。
- **依赖**：SDK-019～SDK-022。

### E. 收尾

### SDK-024：完善 SDK 使用指南

- **目标**：提供完整但简洁的 SDK 用户文档。
- **实现**：说明创建、等待、Run、流式执行、日志、取消、续期、删除和错误处理；更新根 README 入口。
- **依赖**：SDK-023。

### SDK-025：最终回归与验收报告

- **目标**：确认 SDK Phase 可以结束，并记录最终 SHA。
- **验收内容**：执行 `gofmt`、`go test ./...`、`go vet ./...` 和真实 SDK 验收；记录 26 个任务状态、最终能力、已知限制和最终 commit。
- **依赖**：SDK-000～SDK-024。

## 8. 阶段完成标准

全部任务完成后，必须满足：

- 普通用户只导入 `minisandbox/sdk/go` 即可完成核心工作流；
- 创建 sandbox 后可以直接获得资源对象；
- 无需手写轮询即可等待 sandbox 和 execution；
- 无需理解 cursor 和 Base64 即可读取日志；
- 一次 `Run` 可以获得 stdout、stderr、退出码和执行状态；
- 可以通过 SSE 边执行边读取输出；
- 可以通过 SDK 查询 health 和 readiness；
- 可以通过显式方法续期、取消和删除资源；
- README 提供一条清晰、可运行的推荐路径；
- `tests/sdk/go_sdk.go` 使用高层 API 并通过真实服务验收；
- 当前已有底层 SDK 方法没有被无故删除。

## 9. 与 Phase 4 的关系

本阶段只完善当前已经存在的 lifecycle 和 execution 功能。

Phase 4 开发 files、PTY、port proxy 和 capabilities 时，继续在相同对象上增加方法：

```text
Sandbox
  ├─ Files
  ├─ PTY
  ├─ PortHTTP
  └─ Capabilities / WaitReady
```

Phase 4 不需要重新实现 Client、Sandbox、Execution、Wait、Run 和基础错误处理。
