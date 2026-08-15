# SDK Phase 启动前验收清单（SDK-000 / SDK-001）

## 1. 结论

**SDK-000 / SDK-001 状态：PASS。可以开始 SDK-002。**

截至 2026-08-15，当前 `sdk/go` 基线、`7/7 PASS` 真实验收（commit `6708b6c`）
和本阶段推荐 API 列表均已核对。现有 SDK 可以直接作为易用化改造基础，不需要
先行重构或破坏既有底层方法。

## 2. SDK-000：当前基线（验证于 `c6ab7bc`）

### 2.1 现有公开方法

| 方法 | 层面 | 返回 |
|---|---|---|
| `NewClient` | 构造 | `*Client` |
| `CreateSandbox` / `CreateSandboxWithOptions` | 生命周期 | `protocol.Sandbox` |
| `GetSandbox` | 生命周期 | `protocol.Sandbox` |
| `RenewSandbox` | 生命周期 | `protocol.Sandbox` |
| `DeleteSandbox` | 生命周期 | `error` |
| `StartBackgroundExecution` | 执行 | `protocol.ExecutionDescriptor` |
| `GetExecution` | 执行 | `protocol.ExecutionStatus` |
| `GetExecutionLogs` | 执行 | `protocol.ExecutionLogPage` |
| `CancelExecution` | 执行 | `error` |

SDK 原生请求模型：`CreateSandboxRequest`、`SandboxNetworkRequest`、
`RenewSandboxRequest`、`CreateSandboxOptions`、`ExecuteRequest`；错误模型
`ResponseError`；另有 `ExecutionEvent`、`EventType` 两个协议别名。

### 2.2 基线检查结果

| 检查 | 结果 |
|---|---|
| `go test ./sdk/go/...` | PASS |
| `go vet ./sdk/go/...` | PASS |
| `tests/sdk/go_sdk.go` 7/7 真实验收 | PASS（Phase 3 收尾时于 WSL2 + 原生 Docker 执行，`6708b6c`） |

### 2.3 当前主要使用问题

1. 调用方必须手写 sandbox 状态轮询（`tests/sdk/go_sdk.go` 中约 100 行 helper）；
2. 调用方必须维护日志 cursor、校验 sequence 递增并手动解码 Base64；
3. 调用方必须自行判断 sandbox 与 execution 终态；
4. 状态、事件类型散落在 `pkg/protocol`，普通示例需要额外导入；
5. 没有一次调用完成“执行并收集输出”的组合入口；
6. 前台 SSE 执行没有 SDK 封装，调用方需要自行解析 `text/event-stream`；
7. health / readiness 查询没有 SDK 入口。

## 3. SDK-001：推荐使用流程与最终方法列表

推荐流程：`Client` → `Sandbox` 资源对象 → `Execution` 资源对象。

### 3.1 Client

| 方法 | 语义 |
|---|---|
| `NewClient(baseURL, httpClient)` | 保持不变 |
| `Create(ctx, request, ...CreateOption)` | 创建 sandbox 并返回 `*Sandbox`；`WithIdempotencyKey` 可选 |
| `Sandbox(sandboxID)` | 用已知 ID 构造 `*Sandbox` 资源对象 |
| `Health(ctx)` | 映射 `/healthz` 的存活探测 |
| `Readiness(ctx)` | 映射 `/readyz`，返回组件就绪状态 |

### 3.2 Sandbox

| 方法 | 返回 | 语义 |
|---|---|---|
| `ID()` | `string` | 稳定 sandbox 标识 |
| `Info(ctx)` | `SandboxInfo` | 当前生命周期状态 |
| `WaitRunning(ctx)` | `SandboxInfo` | 轮询至 Running；Failed/Terminated 提前失败 |
| `Renew(ctx, expiresAt)` | `SandboxInfo` | 延长租约到绝对时间 |
| `Delete(ctx)` | `error` | 提交删除意图 |
| `DeleteAndWait(ctx)` | `SandboxInfo` | 删除并等待 Terminated |
| `StartExecution(ctx, request)` | `*Execution` | 启动后台 execution |
| `Run(ctx, request)` | `RunResult` | 一次调用完成执行并收集输出 |
| `ExecuteStream(ctx, request)` | 事件迭代器 | 前台 SSE 流式执行 |

### 3.3 Execution

| 方法 | 返回 | 语义 |
|---|---|---|
| `ID()` | `string` | 稳定 execution 标识 |
| `Info(ctx)` | `ExecutionInfo` | 当前执行状态 |
| `Wait(ctx)` | `ExecutionInfo` | 等待任一合法终态 |
| `Logs(ctx, cursor)` | 事件迭代器 | 自动翻页与解码的后台日志 |
| `CancelAndWait(ctx)` | `ExecutionInfo` | 先取消再等待终态 |

### 3.4 类型与错误

- 状态/事件：`SandboxState`、`SandboxReason`、`ExecutionState`、`EventType`
  在 SDK 内直接公开，普通示例不再导入 `pkg/protocol`；
- 信息与结果：`SandboxInfo`、`ExecutionInfo`、`ExecutionEvent`（已解码
  `Data []byte`）、`RunResult`，使用 Go 原生 `time.Time` / `time.Duration`；
- 错误：保留 `ResponseError`，补充少量判断 helper；`Run` 的非零退出、取消、
  超时和执行失败分别定义为可 `errors.As` 的具体错误类型。

## 4. 边界确认

- 现有 9 个底层方法全部保留，不做删除、重命名或语义变更；
- `sdk.ExecutionEvent` 从协议别名改为 SDK 自有的已解码事件类型（当前无
  SDK 外部使用者，属本阶段既定目标，不是破坏性变更）；
- 不新增服务端 API，不修改 OpenAPI 与协议语义；
- 测试集中在 SDK-005 / SDK-011 / SDK-018 / SDK-023 / SDK-025 五个验收时点。

## 5. 已知环境问题（与本阶段无关，最终回归时处理）

`tests/contract` 中 `TestPhase2OperationsGuideMatchesContracts` 仍指向已迁移的
`docs/phase-2-operations-guide.md`（现位于 `docs-dev/dev/`），是文档目录重组
遗留的既有失败，不影响 SDK 包。SDK-025 全量回归前需单独修复并提交。
