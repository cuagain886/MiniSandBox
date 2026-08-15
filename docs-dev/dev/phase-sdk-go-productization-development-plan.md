# SDK Phase：Go 产品 SDK 细粒度开发计划与设计方案

> - 状态：待审查，`SDK-000` 尚未执行
> - 当前基线：Phase 3 已完成，Go SDK 已具备 lifecycle 与后台 execution 的底层方法
> - 前置设计：[Phase 3：可靠性开发计划](./phase-3-reliability-development-plan.md)
> - 后续阶段：[Phase 4：Agent 体验开发计划](./phase-4-agent-experience-development-plan.md)
> - 当前验收：[Go SDK 核心功能验收清单](../getting-started/core-function-acceptance-checklist.md)

## 1. 文档目的

本文把当前 `sdk/go` 从“能够调用 HTTP API 的薄客户端”升级为“成熟、易用、可独立发布的 Go 产品 SDK”，并拆成可以逐项开发、测试、提交和审查的小任务。

本阶段使用独立编号 `SDK-000～SDK-050`，不修改既有 P1～P4 编号。它是 Phase 3 与 Phase 4 之间的产品化轨道：先把现有 lifecycle/execution 能力封装好，Phase 4 再按相同 SDK 架构增加 files、PTY、port proxy 和 capabilities。

执行规则：

1. 一个任务只完成一个可独立审查的目标；
2. 一个任务对应一个 Git commit；
3. 每个任务先运行聚焦测试，再运行该阶段要求的基础检查；
4. 公共 API 先冻结设计，再实现 transport、resource handle 和 convenience API；
5. SDK 只连接 `sandboxd` 公共端点，不访问 runner URL、token 或 Unix Socket；
6. 不为了易用性隐藏危险副作用，删除、取消、重试和自动清理必须有明确语义；
7. 发现公共协议需要变化时，暂停 SDK 任务，先按 API source-of-truth 流程修改 OpenAPI；
8. 任务编号表示依赖顺序，不表示工期。

## 2. 当前基线与问题

### 2.1 已具备能力

当前 `sdk/go` 已提供：

- `NewClient`；
- `CreateSandbox` 与 `CreateSandboxWithOptions`；
- `GetSandbox`、`RenewSandbox`、`DeleteSandbox`；
- `StartBackgroundExecution`、`GetExecution`、`GetExecutionLogs`、`CancelExecution`；
- Go 原生 `time.Duration` 和 `time.Time` 请求字段；
- `ResponseError` 对 HTTP 状态和公共错误详情的保留；
- TTL、timeout 和 `Idempotency-Key` 的部分本地校验；
- 一份真实服务端 `7/7 PASS` 的 SDK 验收程序。

### 2.2 产品化缺口

当前 SDK 仍有以下问题：

- 根模块名是本地 `minisandbox`，没有可供外部项目使用的 canonical module path；
- SDK 与服务端处于同一个根 Go module，使用者会继承不必要的仓库级依赖和发布节奏；
- 公共方法直接返回 `pkg/protocol` 类型，SDK 公共 API 与 wire model 耦合；
- `NewClient` 不返回错误，不能在启动时拒绝非法 base URL 或无效 option；
- 所有资源方法堆在 `Client` 上，没有 `Sandbox`、`Execution` 资源句柄；
- 调用方必须自己写状态轮询、退避、终态判断、日志游标和 Base64 解码；
- 没有同步 `Run`，一次 `echo hello` 也需要多次 SDK 调用；
- 没有前台 SSE execution 客户端；
- 没有 health、readiness 客户端；
- 没有一致的 retry、`Retry-After`、jitter 和幂等安全策略；
- 没有有界输出收集、流关闭和 goroutine 泄漏契约；
- 没有正式的兼容、版本、支持矩阵和发布校验；
- README 示例展示的是底层调用，不是推荐的产品工作流。

## 3. 阶段边界

### 3.1 阶段目标

阶段完成后，普通调用方应能用接近以下形式完成工作：

```go
client, err := minisandbox.NewClient(
    "http://127.0.0.1:8080",
    minisandbox.WithHTTPClient(httpClient),
)
if err != nil {
    return err
}

sandbox, err := client.CreateSandbox(ctx, minisandbox.CreateSandboxRequest{
    Image: "debian:bookworm-slim",
    TTL:   10 * time.Minute,
}, minisandbox.WithIdempotencyKey(jobID))
if err != nil {
    return err
}
defer sandbox.Delete(context.Background())

if _, err := sandbox.WaitRunning(ctx); err != nil {
    return err
}

result, err := sandbox.Run(ctx, minisandbox.Command{
    Argv: []string{"/bin/sh", "-c", "echo hello"},
})
if err != nil {
    return err
}
fmt.Print(string(result.Stdout))
```

产品 SDK 必须同时保留低层能力，供调用方查询快照、分页读取事件、显式控制 cancellation 和消费 SSE。

### 3.2 阶段验收

SDK Phase 必须满足：

- SDK 有独立、真实且可发布的 Go module path；
- 外部临时 module 不使用本地 `replace` 也能完成 release dry-run 安装；
- SDK module 不依赖 Docker、SQLite、Prometheus 或 `internal/**`；
- 公共 API 不暴露根模块的 `pkg/protocol` 类型；
- create → wait running → run → collect output → delete 可在约 30 行内完成；
- 当前公共 OpenAPI 的 health、readiness、lifecycle、前台 SSE 和后台 execution 均有 SDK 能力；
- 所有 wait、retry、stream 和 output collection 都受 context 与明确上限控制；
- 非幂等 execution start 不被自动重试；
- 错误保留 HTTP status、稳定 code、request ID 和 retryable，同时不泄露命令、环境变量或 token；
- `go test`、`go vet`、`go test -race`、contract fixtures 和真实 Linux Docker SDK 验收通过；
- 旧 SDK 调用有明确兼容层或迁移文档；
- SDK API、示例、版本和 changelog 可以独立审查和发布。

### 3.3 明确不做

以下内容不进入本阶段：

- 新增或改变服务端 HTTP、SSE、状态、TTL、取消、删除或鉴权语义；
- files、目录、PTY、port proxy、capabilities 和 image pre-pull；
- TypeScript、Python、浏览器 SDK 或 CORS；
- Pool、快照、pause/resume、Kubernetes、gVisor、Kata 或 microVM；
- 自动启动 Docker、`sandboxd` 或 runner；
- 访问 runner token、Unix Socket、Docker Socket 或宿主机目录；
- 自动生成公网分享 URL；
- 在没有公共鉴权契约前虚构 `WithAPIToken` 产品能力；
- 默认无限重试、无限日志缓存或后台 goroutine 常驻；
- 本阶段直接承诺 v1.0 稳定性。

## 4. 实施前设计门禁

### G1：Canonical module 与发布拓扑

推荐把 `sdk/go` 变成独立 nested module：

```text
<canonical-repository>/sdk/go
```

并采用 Go 子目录 module 的 tag 规则：

```text
sdk/go/v0.1.0
```

当前仓库没有 Git remote，因此不能把占位路径写入 `go.mod`。`SDK-001` 必须先确认真实仓库地址、owner 和 module path，并形成 ADR；这是本阶段唯一必须由仓库归属信息决定的值。

开发期可使用根目录 `go.work` 联调，但发布校验必须在不依赖 `go.work` 和本地 `replace` 的临时 module 中完成。

为了保证每个中间提交都能编译，迁移采用 staging cutover：`SDK-006` 先在临时目录 `sdk/go-next` 建立使用最终 canonical module path 的独立 module，现有 `sdk/go` 在开发期间继续服务根 module 调用方；`SDK-045` 完成 API 后再把新 module 移入最终 `sdk/go`，同步迁移仓库内调用方。`sdk/go-next` 只是开发期目录，不能出现在 tag、文档安装路径或发布产物中。

### G2：公共 API 与资源句柄

推荐冻结以下分层：

```text
Client
  ├─ server health/readiness
  ├─ CreateSandbox(...) -> *Sandbox
  └─ Sandbox(id) -> *Sandbox handle

Sandbox
  ├─ Info / Refresh / WaitRunning
  ├─ Renew / Delete / DeleteAndWait
  ├─ StartExecution -> *Execution
  ├─ ExecuteStream -> EventStream
  └─ Run -> RunResult

Execution
  ├─ Status / Wait
  ├─ Logs -> LogIterator
  └─ Cancel / CancelAndWait
```

资源句柄只保存 immutable ID 和 client 引用，不在本地伪造权威状态。快照使用 `SandboxInfo`、`ExecutionInfo` 等独立值类型。

不提供没有 context 的 `Close()` 来偷偷执行网络删除；资源回收使用显式 `Delete` 或 `DeleteAndWait`。

### G3：SDK model 与 wire model 边界

公共 SDK 类型必须定义在独立 SDK module 中，不能在公共签名中引用：

- `internal/**`；
- 根 module 的 `pkg/protocol`；
- Docker、SQLite 或 HTTP handler 类型。

OpenAPI 仍是公共协议 source of truth。SDK 内部可使用私有 `internal/wire` 类型和显式转换，并通过共享 JSON/SSE fixtures 防止漂移。未知响应字段应向后兼容忽略；必填字段、枚举、事件 sequence 和终态组合必须严格验证。

### G4：Retry 与幂等安全

推荐默认采用有界重试，但只允许：

- GET health/readiness/status/logs；
- 带 `Idempotency-Key` 的 create；
- 使用绝对 `expires_at` 的 renew；
- 服务端已经定义为幂等的 delete/cancel。

不得自动重试：

- 无幂等 key 的 create；
- execution start，包括前台 SSE 与后台 execution；
- 已经开始交付 body/event 的 stream。

重试只针对明确的暂时性 transport 错误、`429`、`502`、`503`、`504` 或公共错误 `retryable=true`。使用 capped exponential backoff、full jitter、`Retry-After` 和 context deadline；默认次数、最小/最大间隔必须在 `SDK-004` 冻结。

### G5：Wait、取消与清理

- `WaitRunning` 只等待 lifecycle `Running`；Phase 4 的 `WaitReady` 保留给“Running + capabilities”语义；
- wait 遇到明确终态立即返回 typed error，不等到 context 超时；
- 所有 wait 使用 context 控制总时长，不再额外制造隐藏的无限 timeout；
- `Delete` 只提交意图，`DeleteAndWait` 才等待 `Terminated`；
- `Cancel` 只提交取消，`CancelAndWait` 才等待 execution 终态；
- 普通 wait 失败不自动删除 sandbox；
- `Run` 的 context 取消默认请求取消其 execution，但该行为必须可配置且有界；
- SDK 不承诺服务端进程崩溃后的 execution 恢复。

### G6：Stream、日志与内存边界

- 前台 SSE 使用显式 iterator/stream，不使用无法关闭的裸 channel；
- `LogIterator` 按 cursor 拉取，不创建永久后台 goroutine；
- 高层事件自动把 Base64 解码为 `[]byte`，仍保留 stdout/stderr 类型；
- `CollectLogs` 与 `Run` 必须有客户端最大收集字节数；零值不能表示无限；
- 达到客户端上限时返回部分结果和 typed limit error，不把输出放入 error string；
- 服务端 `output_truncated` 与客户端 collection limit 必须区分；
- response body、SSE stream、timer 和 ticker 在成功、失败、取消路径都关闭。

### G7：Transport、鉴权与秘密

- `NewClient` 校验 scheme、host、path、query、fragment 和 userinfo；本地开发允许显式 HTTP；
- 认证、mTLS、代理和 tracing 通过调用方注入的 `http.Client`/`RoundTripper` 扩展；
- 在服务端公共鉴权协议冻结前不提供含糊的 token option；
- SDK 自身不记录 request/response body、命令、env、header 或 URL userinfo；
- User-Agent、request ID 和 operation 名可以进入可选 hook；
- hook 不得获得 Authorization、Cookie 或原始命令输出；
- 错误解析和日志必须有 body 大小上限。

### G8：版本、兼容与支持矩阵

- 初始产品化版本采用 `v0.x` SemVer；
- Go 最低版本与仓库当前 baseline 一致，后续降低版本必须单独验证；
- patch 版本不增加破坏性变化，minor 版本可以在 v0 期间按迁移文档演进；
- 已发布公共标识符必须进入 API snapshot/diff 检查；
- 当前旧方法尽量通过 deprecated wrapper 保留一个迁移窗口；
- 无法兼容的 module path 与返回类型变化必须在迁移指南中逐项列出；
- Phase 4 只扩展现有资源句柄，不重新创建第二套 lifecycle/execution facade。

## 5. 目标模块与依赖结构

推荐结构：

```text
sdk/go/
  go.mod
  client.go
  options.go
  errors.go
  server.go
  sandbox.go
  execution.go
  stream.go
  wait.go
  retry.go
  hooks.go
  compat.go
  internal/wire/
  internal/testserver/
  examples/
  README.md

tests/contract/sdk/
  lifecycle/
  execution/
  errors/
  sse/
```

依赖方向：

```text
用户程序
  -> SDK public facade
      -> resource handles
          -> transport / wait / retry
              -> private wire model
                  -> sandboxd public HTTP/SSE
```

SDK module 优先只使用 Go 标准库。任何新增生产依赖都必须单独 ADR，说明维护状态、许可证、体积、安全历史和不可由标准库满足的理由。

## 6. 关键产品语义

### 6.1 低层与高层 API 同时存在

低层 API 一次调用对应一次公共服务端操作，方便高级调用方精确控制；高层 API 组合低层操作，但不能改变服务端事实：

| 高层方法 | 组合语义 |
|---|---|
| `WaitRunning` | 重复 `GetSandbox`，直到 Running、终态或 context 结束 |
| `DeleteAndWait` | `Delete` 后等待 Terminated |
| `Execution.Wait` | 重复获取 execution，直到唯一终态 |
| `CancelAndWait` | `Cancel` 后等待 execution 终态 |
| `Run` | start background → wait terminal → collect logs → map result/error |
| `ExecuteStream` | 单次前台 SSE 请求 → 严格事件 iterator |

### 6.2 `Run` 返回值

推荐：

```go
type RunResult struct {
    ExecutionID      string
    State            ExecutionState
    ExitCode         int
    Stdout           []byte
    Stderr           []byte
    Duration         time.Duration
    OutputTruncated  bool
    CollectionLimited bool
}
```

- exit code 0：返回 result 与 `nil`；
- 非零 exit：返回 result 与可 `errors.As` 的 `*ExitError`；
- Failed、Cancelled、TimedOut：返回尽可能完整的 result 与对应 typed error；
- transport/protocol 错误：返回已安全获得的部分 result 与错误；
- `Error()` 不能包含 stdout、stderr、argv、shell 或 env。

### 6.3 零值和默认值

- duration、output limit、poll policy 等不能用模糊零值表示无限；
- `CreateSandboxRequest.TTL == 0` 表示由服务端选择默认 TTL；
- execution timeout 为零表示服务端默认值，与当前 wire 兼容；
- collection limit 使用明确默认值，并提供有上限的 option；
- `Idempotency-Key` 为空表示明确的非幂等 create，SDK 不自动生成业务 key；
- SDK 可以提供随机 key helper，但调用方必须显式选择使用。

## 7. 每个任务的完成标准

除任务自身验收项外，每个代码任务都必须：

- 保持中文 package 注释和导出 API Go doc 同步；
- 使用 `gofmt`；
- 运行 SDK module 的聚焦测试和 `go vet`；
- 运行受影响的根 module contract tests；
- 涉及 iterator、timer、retry、hook 或并发时运行 `go test -race`；
- 涉及 JSON、URL、SSE 或 cursor parser 时增加 fuzz/property tests；
- 不把 sleep、真实网络或随机抖动写进普通单元测试；
- 使用 fake clock、fake random、`httptest.Server` 和可控 response body；
- 验证所有 response body、timer、ticker 和 iterator 均被关闭；
- 公共 API 变化同步 API snapshot、示例和迁移文档；
- 不新增 Docker、SQLite、Prometheus 或服务端内部依赖；
- 显式暂存本任务文件，并创建唯一目标的 commit。

阶段级基础检查：

```bash
go test ./...
go vet ./...

cd sdk/go
go test ./...
go vet ./...
go test -race ./...
```

真实服务端相关任务还必须在 Linux/amd64 + Docker 环境运行 SDK acceptance，不得用 mock 通过代替。

## 8. 任务总览

| 分组 | 任务 | 结果 |
|---|---:|---|
| A. 基线与设计冻结 | SDK-000～SDK-005 | 冻结 module、API、model、retry、wait 和安全语义 |
| B. 独立 Module 与 HTTP Core | SDK-006～SDK-010 | 建立可独立构建、校验和解析响应的 transport |
| C. 公共 Model 与错误 | SDK-011～SDK-015 | SDK 自有类型、typed errors、request metadata 和测试基座 |
| D. 公共协议低层能力 | SDK-016～SDK-020 | lifecycle、后台 execution、SSE、server probe 和 fixtures |
| E. 资源句柄与等待器 | SDK-021～SDK-025 | Sandbox/Execution handle、poll policy 和 wait |
| F. 取消、删除与资源安全 | SDK-026～SDK-030 | delete/cancel-and-wait、续期、并发安全和 handle workflow |
| G. 日志与同步 Run | SDK-031～SDK-035 | decoded events、iterator、有界收集和 Run happy path |
| H. Run 终态与流式执行 | SDK-036～SDK-040 | context、终态错误、截断、SSE 消费和泄漏测试 |
| I. Retry、观测与兼容 | SDK-041～SDK-045 | 安全重试、退避、hook、脱敏和旧 API 迁移 |
| J. 验收、文档与发布 | SDK-046～SDK-050 | 测试矩阵、真实验收、示例、release dry-run 和最终报告 |

## 9. 详细任务

### A. 基线与设计冻结

### SDK-000：验证当前 SDK 基线

- **依赖**：Phase 3 验收、当前 SDK `7/7 PASS` 程序。
- **唯一目标**：记录产品化开始前的公共 API、module 状态、测试结果和已知缺口。
- **设计**：保存 commit SHA、`go doc`、依赖列表、OpenAPI operation matrix、SDK acceptance 输出和工作树状态。
- **修改范围**：SDK Phase kickoff checklist/report，不修改生产代码。
- **测试**：根 module tests、SDK package tests、真实 `go run ./tests/sdk`。
- **验收**：每个当前操作都有基线结果，失败和未覆盖项没有被记为 PASS。
- **本任务不做**：不重构 SDK。

### SDK-001：冻结 canonical module path 与 release topology

- **依赖**：SDK-000、G1。
- **唯一目标**：确认真实 repository URL、nested module path 和 tag 前缀。
- **设计**：形成 ADR，明确 `sdk/go/go.mod` module、`sdk/go/vX.Y.Z` tag、Go proxy 行为、仓库迁移规则和 ownership。
- **修改范围**：ADR 与本计划状态。
- **测试**：用候选 module path 执行临时 module import proof，不发布 tag。
- **验收**：不存在 `example.com`、本地盘符、`minisandbox` 裸 module 或依赖永久 `replace` 的方案。
- **本任务不做**：不创建 nested module。

### SDK-002：冻结产品公共 API

- **依赖**：SDK-001、G2。
- **唯一目标**：确定 Client、Sandbox、Execution、iterator 与 Run 的公共签名。
- **设计**：写 API proposal 和可编译 compile-only 示例；明确 context、option、返回值、资源所有权和命名。
- **修改范围**：SDK API design 文档与 API fixture。
- **测试**：proposal compile test。
- **验收**：普通工作流不需要直接接触 URL path、Base64、cursor 或 protocol DTO。
- **本任务不做**：不实现 HTTP。

### SDK-003：冻结公共 model 与兼容策略

- **依赖**：SDK-002、G3、G8。
- **唯一目标**：确定 SDK 自有 model、wire 转换和旧 API 迁移边界。
- **设计**：列出 SandboxInfo、ExecutionInfo、Event、Command、RunResult、options；标记旧标识符保留、deprecated 或不可兼容迁移。
- **修改范围**：model/compatibility ADR 与 API snapshot。
- **测试**：公共类型不引用根 module package 的静态检查原型。
- **验收**：wire 增加向后兼容字段不会迫使 SDK 公共类型破坏性变化。
- **本任务不做**：不复制 handler/domain 类型。

### SDK-004：冻结 retry、wait 与 cleanup 语义

- **依赖**：SDK-002、G4、G5。
- **唯一目标**：把所有 operation 的幂等性、重试、轮询、取消和清理行为写成矩阵。
- **设计**：冻结默认 retry 次数、退避上下限、jitter、Retry-After、wait poll policy、Run cancel-on-context 和显式 cleanup 规则。
- **修改范围**：SDK behavior contract。
- **测试**：表驱动决策矩阵原型。
- **验收**：每个方法都能回答“是否会重试、是否有副作用、何时停止、是否自动清理”。
- **本任务不做**：不实现 retry loop。

### SDK-005：冻结错误、安全与观测语义

- **依赖**：SDK-003、SDK-004、G6、G7。
- **唯一目标**：确定 typed error、输出边界、hook 字段和秘密处理。
- **设计**：冻结 ResponseError、ProtocolError、WaitError、ExitError、LimitError；规定 Error 文本、request ID、retryable、partial result 和 redaction。
- **修改范围**：错误与 SDK threat model 文档。
- **测试**：错误字符串和 hook payload 的泄漏测试原型。
- **验收**：argv、shell、env、输出、token、URL userinfo 不会进入 error/hook。
- **本任务不做**：不引入 telemetry 依赖。

### B. 独立 Module 与 HTTP Core

### SDK-006：创建独立 Go module staging

- **依赖**：SDK-001～SDK-005。
- **唯一目标**：在不破坏当前 SDK 调用方的前提下，按 canonical path 创建 `sdk/go-next/go.mod`。
- **设计**：staging module 使用最终 module path且初始只依赖标准库；现有 `sdk/go` 暂时不动；根仓库通过 `go.work` 或显式 CI workspace 联调。
- **修改范围**：`sdk/go-next/go.mod`、workspace 与最小 package scaffold。
- **测试**：在 SDK 目录独立执行 `go list -deps`、`go test`、`go vet`。
- **验收**：依赖图不包含 Docker、SQLite、Prometheus、根 module `internal/**`。
- **本任务不做**：不迁移现有实现。

### SDK-007：建立 SDK package layout 与依赖守卫

- **依赖**：SDK-006。
- **唯一目标**：建立 public facade、`internal/wire` 和 `internal/testserver` 边界。
- **设计**：增加依赖守卫测试，禁止导入根 module protocol/internal 和未批准第三方依赖。
- **修改范围**：SDK 目录骨架与 architecture test。
- **测试**：package list/import graph 测试。
- **验收**：依赖方向只能由 public facade 指向 SDK internal 包。
- **本任务不做**：不增加业务方法。

### SDK-008：实现 Client 构造与 options

- **依赖**：SDK-007。
- **唯一目标**：实现返回 `(*Client, error)` 的严格构造器。
- **设计**：支持 base URL、注入 HTTP client、User-Agent suffix、poll/retry policy；复制 option 输入，避免调用后可变共享。
- **修改范围**：client/options。
- **测试**：默认值、nil client、非法 option、并发只读。
- **验收**：Client 构造后可安全并发使用，错误在首次请求前暴露。
- **本任务不做**：不发 HTTP 请求。

### SDK-009：实现安全 URL 与 request builder

- **依赖**：SDK-008、G7。
- **唯一目标**：安全构造固定公共 endpoint 请求。
- **设计**：校验 scheme/host/userinfo/path/query/fragment；resource ID 使用 path escaping；固定 header 由 SDK 控制。
- **修改范围**：transport request builder。
- **测试**：IPv4/IPv6、subpath policy、恶意 ID、userinfo、encoded slash、query injection 和 fuzz。
- **验收**：调用方数据不能改变 endpoint 层级或注入 header/query。
- **本任务不做**：不实现 response decode。

### SDK-010：实现有界 HTTP response core

- **依赖**：SDK-009。
- **唯一目标**：统一处理成功 JSON、无 body 响应和错误 envelope。
- **设计**：成功/错误 body 各有上限；所有路径关闭 body；保留 status/header/request ID；未知响应字段向后兼容。
- **修改范围**：transport response core。
- **测试**：空 body、超限、截断 JSON、错误 content type、取消、close tracking。
- **验收**：畸形或无限响应不能造成无限内存或连接泄漏。
- **本任务不做**：不映射具体资源 model。

### C. 公共 Model 与错误

### SDK-011：实现 SDK lifecycle model

- **依赖**：SDK-003、SDK-010。
- **唯一目标**：实现 SDK 自有 create、network、sandbox state/info 和 renew 类型。
- **设计**：使用 Go 原生 duration/time；枚举保持 wire 值但不做 type alias；私有 wire conversion 显式处理 field presence。
- **修改范围**：public lifecycle model 与 private wire model。
- **测试**：JSON fixtures、TTL 边界、UTC、outbound false/missing、未知字段。
- **验收**：公共签名不引用 `pkg/protocol`。
- **本任务不做**：不发送 lifecycle 请求。

### SDK-012：实现 SDK execution model

- **依赖**：SDK-003、SDK-010。
- **唯一目标**：实现 Command、ExecutionInfo、state、event 和 terminal model。
- **设计**：argv/shell 二选一；duration 单位显式；高层 Event 使用 decoded bytes，private wire 保留 Base64。
- **修改范围**：public execution model 与 private wire model。
- **测试**：全部事件类型、非法组合、Base64、sequence、退出码和 fuzz。
- **验收**：无效 server event 返回 ProtocolError，不能伪装为用户输出。
- **本任务不做**：不实现 SSE parser。

### SDK-013：实现统一 typed errors

- **依赖**：SDK-005、SDK-010～SDK-012。
- **唯一目标**：实现稳定、可 `errors.As/Is` 的 SDK 错误体系。
- **设计**：ResponseError 暴露 StatusCode/Code/RequestID/Retryable；增加 ValidationError、ProtocolError；Error 文本使用安全固定信息。
- **修改范围**：errors 与 mapper。
- **测试**：errors.As/Is、HTTP/error code matrix、空 request ID、错误字符串脱敏。
- **验收**：调用方不解析字符串即可分支处理。
- **本任务不做**：不增加每个服务端错误一个 Go 类型。

### SDK-014：实现 request metadata 与安全扩展点

- **依赖**：SDK-008～SDK-013、G7。
- **唯一目标**：增加 User-Agent、调用方 request ID 和受限 request hook。
- **设计**：hook 只接收 operation、method、status、duration、request ID 和 retry attempt；认证继续由 HTTP transport 注入。
- **修改范围**：client option、request metadata、hook interface。
- **测试**：并发 hook、panic policy、header precedence、秘密不可见。
- **验收**：SDK 可观测但不暴露 body、output 或敏感 header。
- **本任务不做**：不依赖 OpenTelemetry SDK。

### SDK-015：建立确定性 HTTP 测试基座

- **依赖**：SDK-007～SDK-014。
- **唯一目标**：提供可复用的 fake server、body close tracker、fake clock 和 fake random。
- **设计**：每个测试声明期望 method/path/header/body 和响应序列；未消费请求立即失败。
- **修改范围**：SDK internal test utilities。
- **测试**：基座自测、并发和取消。
- **验收**：后续 retry/wait/stream 测试不依赖真实 sleep 或公网。
- **本任务不做**：不模拟 Docker。

### D. 公共协议低层能力

### SDK-016：实现 lifecycle 低层方法

- **依赖**：SDK-011、SDK-013～SDK-015。
- **唯一目标**：实现 create/get/renew/delete 的一请求一操作 API。
- **设计**：create option 显式传幂等 key；renew 只接受绝对 expiry；delete 不等待终态。
- **修改范围**：Client lifecycle methods。
- **测试**：OpenAPI fixtures、headers、路径、状态码、context 和本地校验。
- **验收**：每个方法与公共 OpenAPI operation 一一对应。
- **本任务不做**：不实现 wait/retry。

### SDK-017：实现后台 execution 低层方法

- **依赖**：SDK-012～SDK-015。
- **唯一目标**：实现 start/status/cancel/log-page 的一请求一操作 API。
- **设计**：start 永不隐式重试；logs 暴露显式 cursor/page limit；cancel 不等待终态。
- **修改范围**：Client execution methods。
- **测试**：argv/shell/env/cwd/timeout、ID escaping、terminal status、cursor/page。
- **验收**：后台 execution 全部公共 endpoint 可被独立调用。
- **本任务不做**：不实现 iterator 或 Run。

### SDK-018：实现前台 execution SSE 低层客户端

- **依赖**：SDK-012～SDK-015、Phase 2 SSE contract。
- **唯一目标**：以可关闭 iterator 消费 `text/event-stream` execution。
- **设计**：严格解析 frame/event/data，验证 sequence 和唯一 terminal；限制 frame/event bytes；关闭即取消请求。
- **修改范围**：SSE transport 与 EventStream。
- **测试**：split read、CRLF、注释、未知 event、超限、重复 sequence、双 terminal、断流、close。
- **验收**：不创建不可回收 goroutine，terminal 后不再产出事件。
- **本任务不做**：不自动收集全部输出。

### SDK-019：实现 health 与 readiness 客户端

- **依赖**：SDK-010、SDK-013～SDK-015。
- **唯一目标**：覆盖 `/healthz` 和 `/readyz` 公共契约。
- **设计**：返回 typed server/readiness snapshot；503 readiness 保留 component 状态而不是丢成普通字符串。
- **修改范围**：server client/model。
- **测试**：ready、not ready、未知 component、超限和 context。
- **验收**：调用方能区分服务进程存活与依赖就绪。
- **本任务不做**：不访问 admin diagnostics/metrics。

### SDK-020：建立 SDK contract fixture matrix

- **依赖**：SDK-016～SDK-019。
- **唯一目标**：用同一组 fixture 锁定 SDK 与 OpenAPI 的当前公共能力。
- **设计**：覆盖 lifecycle、execution、SSE、errors、health/readiness；fixture 带 schema version 和来源说明。
- **修改范围**：`tests/contract/sdk` 与 SDK conformance tests。
- **测试**：fixture schema/hash 自检及全部 decode/encode。
- **验收**：OpenAPI 字段、枚举、单位或错误漂移会使测试失败。
- **本任务不做**：不引入代码生成器。

### E. 资源句柄与等待器

### SDK-021：实现 Sandbox 资源句柄

- **依赖**：SDK-002、SDK-016。
- **唯一目标**：把 sandbox ID 与 Client 组合成并发安全 handle。
- **设计**：Create 返回 handle；`Client.Sandbox(id)` 只构造引用；`Info/Refresh` 每次读取服务端权威快照。
- **修改范围**：Sandbox facade。
- **测试**：ID、nil client 防护、并发 refresh、404。
- **验收**：handle 不缓存并冒充权威状态。
- **本任务不做**：不实现 wait。

### SDK-022：实现 Execution 资源句柄

- **依赖**：SDK-017、SDK-021。
- **唯一目标**：通过 Sandbox 启动和引用 execution handle。
- **设计**：handle 固定 sandbox/execution ID；Status/Cancel/LogPage 委托低层方法；不暴露 runner 标识。
- **修改范围**：Execution facade。
- **测试**：归属 ID、路径、并发 status/cancel、404。
- **验收**：不能把一个 execution 错接到另一个 sandbox。
- **本任务不做**：不实现 wait/iterator。

### SDK-023：实现 PollPolicy 与退避原语

- **依赖**：SDK-004、SDK-015。
- **唯一目标**：为 wait 和 logs 建立可测试的轮询策略。
- **设计**：最小/最大 interval、multiplier、full jitter；context 是总 deadline；注入 clock/rng；禁止非正值。
- **修改范围**：poll/backoff primitives。
- **测试**：序列、cap、jitter 边界、cancel、无 timer 泄漏。
- **验收**：单元测试零真实 sleep。
- **本任务不做**：不判断业务终态。

### SDK-024：实现 Sandbox.WaitRunning

- **依赖**：SDK-021、SDK-023、G5。
- **唯一目标**：等待 sandbox 进入 Running 或明确失败。
- **设计**：Failed/Terminated 立即 typed error；保留最后 snapshot；context 结束返回 context cause。
- **修改范围**：sandbox waiter。
- **测试**：Pending→Creating→Running、Failed、Terminated、404、timeout、transient read。
- **验收**：不使用固定一秒循环，且不把 WaitRunning 命名成 Phase 4 WaitReady。
- **本任务不做**：不查询 capabilities。

### SDK-025：实现 Execution.Wait

- **依赖**：SDK-022、SDK-023。
- **唯一目标**：等待 execution 的唯一终态。
- **设计**：Exited/Failed/Cancelled/TimedOut 均返回完整终态 snapshot；协议非法时立即失败。
- **修改范围**：execution waiter。
- **测试**：所有状态转换、未知状态、terminal event 缺失/不匹配、context。
- **验收**：非零 exit 的 Exited 仍先作为有效终态返回，由高层 Run 决定错误语义。
- **本任务不做**：不收集日志。

### F. 取消、删除与资源安全

### SDK-026：实现 DeleteAndWait

- **依赖**：SDK-024。
- **唯一目标**：显式提交删除并等待 Terminated。
- **设计**：重复删除成功；保留最后 snapshot；context 结束不伪装删除完成；不使用 background context 无限清理。
- **修改范围**：Sandbox deletion convenience。
- **测试**：202→Terminated、已终态、重复、失败、context。
- **验收**：`Delete` 与 `DeleteAndWait` 副作用差异在 API 和文档中明显。
- **本任务不做**：不提供无 context Close。

### SDK-027：实现 CancelAndWait

- **依赖**：SDK-025。
- **唯一目标**：显式取消 execution 并等待稳定终态。
- **设计**：取消竞争允许先观察到其他合法终态；方法返回真实胜出终态，不伪造 Cancelled。
- **修改范围**：Execution cancellation convenience。
- **测试**：Cancelled、Exited race、TimedOut race、重复取消、context。
- **验收**：终态竞争与服务端一致。
- **本任务不做**：不杀宿主机进程。

### SDK-028：实现 Sandbox.RenewUntil

- **依赖**：SDK-016、SDK-021。
- **唯一目标**：在 handle 上提供绝对时间续期并更新返回 snapshot。
- **设计**：只接受非零绝对时间；不提供会在重试时重复累加的隐式 `RenewFor`。
- **修改范围**：Sandbox renewal facade。
- **测试**：UTC、equal no-op、shorten conflict、expired/terminating。
- **验收**：SDK 重试不会改变请求的绝对 expiry。
- **本任务不做**：不实现客户端续租守护 goroutine。

### SDK-029：验证 handle 并发安全与资源所有权

- **依赖**：SDK-021～SDK-028。
- **唯一目标**：确认 handle 可并发查询且 iterator/stream 所有权明确。
- **设计**：不可变 ID/client；不共享可变 snapshot；每个 stream 由创建者 Close；文档声明 concurrent method matrix。
- **修改范围**：race tests 与所有权文档。
- **测试**：`go test -race`、并发 status/refresh/cancel/delete。
- **验收**：无 data race、double close panic 或跨资源状态污染。
- **本任务不做**：不保证并发业务操作一定成功。

### SDK-030：建立 handle 推荐工作流示例

- **依赖**：SDK-021～SDK-029。
- **唯一目标**：用 handle 完成 create→wait→start→wait→delete。
- **设计**：示例可编译；失败路径显式 best-effort cleanup；不使用 panic 作为库级错误处理示范。
- **修改范围**：SDK examples/tests。
- **测试**：example test 和 fake server workflow。
- **验收**：示例不接触 HTTP path、protocol DTO 或 Base64。
- **本任务不做**：不使用尚未实现的 Run。

### G. 日志与同步 Run

### SDK-031：实现 decoded execution event

- **依赖**：SDK-012、SDK-017。
- **唯一目标**：向使用者提供已解码且类型安全的 stdout/stderr event。
- **设计**：Data 为新分配或不可变 bytes；保留 sequence/timestamp；terminal 字段按事件类型约束。
- **修改范围**：event conversion/validation。
- **测试**：二进制零字节、非 UTF-8、大 chunk、Base64 错误、字段组合。
- **验收**：使用者不需要调用 Base64 decoder。
- **本任务不做**：不把 bytes 转成 string。

### SDK-032：实现 LogIterator

- **依赖**：SDK-022、SDK-023、SDK-031。
- **唯一目标**：从 cursor 开始按页输出有序 decoded event。
- **设计**：pull-based `Next`；空页按 policy 等待；Complete 后 EOF；Close 幂等；严格防回退/重复 sequence。
- **修改范围**：log iterator。
- **测试**：多页、空页、cursor、complete、重复、倒序、cancel、close。
- **验收**：无后台 goroutine且内存与单页大小成正比。
- **本任务不做**：不自动拼接 stdout/stderr。

### SDK-033：实现有界 LogCollector

- **依赖**：SDK-032、G6。
- **唯一目标**：按 stdout/stderr 分离收集日志，并强制客户端字节上限。
- **设计**：分别统计总量；达到上限停止保存但继续取得终态所需信息；返回 partial output 和 LimitError。
- **修改范围**：collector/options。
- **测试**：边界前/等于/超过、二进制、服务端 truncated、客户端 limited。
- **验收**：零值配置不会导致无限缓存。
- **本任务不做**：不写本地文件。

### SDK-034：实现 RunResult 与 execution typed errors

- **依赖**：SDK-005、SDK-025、SDK-033。
- **唯一目标**：冻结同步 Run 的返回结果和终态错误类型。
- **设计**：ExitError、FailedError、CancelledError、TimeoutError 持有安全 metadata/partial result；Error 文本不带输出。
- **修改范围**：Run model/errors。
- **测试**：errors.As、非零 exit、partial result、脱敏、duration。
- **验收**：调用方既可按错误类型分支，也能读取已获得的输出。
- **本任务不做**：不启动 execution。

### SDK-035：实现 Sandbox.Run 正常路径

- **依赖**：SDK-022、SDK-025、SDK-033、SDK-034。
- **唯一目标**：组合后台 start、wait 和 logs，完成同步命令执行。
- **设计**：start 只发一次；并行轮询不引入泄漏；最终校验唯一 terminal；exit 0 返回完整 RunResult。
- **修改范围**：Sandbox Run convenience。
- **测试**：stdout/stderr、无输出、多 chunk、env/cwd/timeout、退出 0。
- **验收**：`echo hello` 工作流无需调用方手写轮询、cursor 或 Base64。
- **本任务不做**：不处理 context 后的主动 cancel。

### H. Run 终态与流式执行

### SDK-036：实现 Run context cancellation

- **依赖**：SDK-027、SDK-035、G5。
- **唯一目标**：context 结束时按 option 有界请求取消 execution。
- **设计**：默认 cancel-on-context；使用固定短 cleanup timeout；返回原 context cause 和可用 partial result；不得无限等待。
- **修改范围**：Run cancellation path/options。
- **测试**：cancel 前/后 start、cancel 请求失败、终态竞争、cleanup timeout。
- **验收**：无遗留 SDK goroutine，且不把 cancel 失败覆盖原 context 错误。
- **本任务不做**：不保证服务端不可用时进程立即消失。

### SDK-037：实现 Run 全终态映射

- **依赖**：SDK-034～SDK-036。
- **唯一目标**：正确映射非零 Exited、Failed、Cancelled 和 TimedOut。
- **设计**：结果与错误同时返回；服务端终态优先于较晚到达的本地 context；协议不一致为 ProtocolError。
- **修改范围**：Run terminal mapper。
- **测试**：全部状态、race 顺序、terminal event 缺失、exit code 边界。
- **验收**：任何终态都不会被误报为成功或通用字符串错误。
- **本任务不做**：不自动重跑失败命令。

### SDK-038：区分服务端截断与客户端收集上限

- **依赖**：SDK-033、SDK-037。
- **唯一目标**：把两类 output loss 作为不同、可观测结果交付。
- **设计**：RunResult 分别记录 OutputTruncated 与 CollectionLimited；partial bytes 仍返回；错误优先级固定。
- **修改范围**：collector/result/error mapping。
- **测试**：四种组合、终态错误叠加、边界字节。
- **验收**：调用方能判断应调大客户端限制还是服务端限制。
- **本任务不做**：不修改 runner 输出上限。

### SDK-039：完善 ExecuteStream 产品接口

- **依赖**：SDK-018、SDK-031、SDK-034。
- **唯一目标**：在 Sandbox handle 上交付前台 SSE 流式执行。
- **设计**：stream 返回 decoded Event；断开遵循服务端前台取消语义；terminal exactly once；提供显式 Close。
- **修改范围**：Sandbox streaming facade。
- **测试**：实时 stdout/stderr、terminal、消费者提前关闭、慢消费者和 server disconnect。
- **验收**：调用方可边执行边消费输出，不必理解 SSE frame。
- **本任务不做**：不把 SSE 包装成无限缓冲 channel。

### SDK-040：执行 stream/wait/run 泄漏与模糊测试

- **依赖**：SDK-023～SDK-039。
- **唯一目标**：验证 timer、body、iterator 和并发路径没有泄漏或竞态。
- **设计**：close tracker、goroutine baseline、race、SSE/event/cursor fuzz；避免脆弱固定 goroutine 数断言。
- **修改范围**：SDK robustness tests。
- **测试**：`go test -race`、fuzz seeds、重复 cancel/close、随机状态序列。
- **验收**：所有失败路径释放资源，parser 不 panic。
- **本任务不做**：不运行 Docker。

### I. Retry、观测与兼容

### SDK-041：实现 retry classifier

- **依赖**：SDK-004、SDK-013。
- **唯一目标**：根据 operation、幂等上下文、transport 和 ResponseError 决定是否可重试。
- **设计**：决策为纯函数；execution start 和已交付 stream 永远 false；create 必须证明存在有效 key。
- **修改范围**：retry classifier。
- **测试**：完整 operation/error/status/retryable 矩阵。
- **验收**：任何未知情况默认不重试。
- **本任务不做**：不 sleep 或发送请求。

### SDK-042：实现 capped backoff 与 Retry-After

- **依赖**：SDK-015、SDK-023、SDK-041。
- **唯一目标**：提供 context-aware、可确定性测试的 retry engine。
- **设计**：full jitter、cap、attempt limit；解析秒数/HTTP date Retry-After；服务端值仍受本地最大等待限制。
- **修改范围**：retry loop/backoff parser。
- **测试**：attempt、cap、日期、非法 header、context、fake clock/rng。
- **验收**：无真实 sleep 测试且不会越过 context deadline。
- **本任务不做**：不接入业务 operation。

### SDK-043：把 retry 接入安全 operation

- **依赖**：SDK-016～SDK-019、SDK-041、SDK-042。
- **唯一目标**：只对 G4 允许的操作启用统一 retry。
- **设计**：每次 attempt 重新构造可重放 body；保留同一业务幂等 key、生成新的传输 request；hook 记录 attempt。
- **修改范围**：transport operation wrappers。
- **测试**：GET、带/不带 key create、renew、delete、cancel、start、stream body delivered。
- **验收**：start execution 和无 key create 在模糊网络错误下最多发送一次。
- **本任务不做**：不重试用户命令。

### SDK-044：完善 operation hooks 与脱敏

- **依赖**：SDK-014、SDK-043。
- **唯一目标**：为请求耗时、重试和结果提供稳定的无秘密观测接口。
- **设计**：begin/end hook；固定 operation enum 和 result enum；panic 隔离策略按 SDK-005；不记录 ID 时提供可选 hash 而非原值。
- **修改范围**：hooks 与 redaction tests。
- **测试**：成功/错误/retry/cancel、并发、hook panic、secret sentinels。
- **验收**：hook payload 无 image credential、idempotency key、sandbox/execution ID、command/env/output/token。
- **本任务不做**：不注册全局 metrics/tracer。

### SDK-045：执行最终目录 cutover 并提供兼容迁移

- **依赖**：SDK-003、SDK-016～SDK-044。
- **唯一目标**：把完成的 staging module 移入最终 `sdk/go`，并让现有仓库调用方有受控迁移路径。
- **设计**：迁移仓库内 import；能保留的旧方法放入 `compat.go` 并标记 Deprecated；本地裸 module path 和 protocol 类型等无法跨 module 保留的部分通过 migration adapter/example 说明；删除空的 staging 路径。
- **修改范围**：`sdk/go-next` → `sdk/go` cutover、compat wrappers、仓库内调用方、compile tests 和迁移清单。
- **测试**：旧 `tests/contract` 与旧示例的 compile compatibility。
- **验收**：每个旧公共标识符都有“保留/替代/移除原因”记录。
- **本任务不做**：不永久维护两套实现。

### J. 验收、文档与发布

### SDK-046：完成 SDK 单元、contract 与 API snapshot 矩阵

- **依赖**：SDK-006～SDK-045。
- **唯一目标**：建立发布前自动测试总入口。
- **设计**：覆盖 public API、wire fixtures、errors、wait、retry、Run、SSE、race 和 fuzz seed；保存导出 API snapshot。
- **修改范围**：SDK CI/test scripts 与 contract tests。
- **测试**：SDK module 全测试、root contract tests、race。
- **验收**：公共标识符或 wire 漂移会产生可审查失败。
- **本任务不做**：不访问 Docker。

### SDK-047：把真实 SDK 验收迁移到产品 API

- **依赖**：SDK-026～SDK-046。
- **唯一目标**：使用 Client/Sandbox/Execution/Run 重写当前 `tests/sdk` 验收。
- **设计**：保留 S01～S07 语义；新增 health/readiness、Run 与 SSE；每次唯一 key；失败路径清理。
- **修改范围**：live SDK acceptance 与验收文档。
- **测试**：Linux/amd64 + Docker 真服务端运行。
- **验收**：不直接使用 protocol DTO、cursor、Base64 或手写轮询仍全部 PASS。
- **本任务不做**：不以 mock 结果替代真实验收。

### SDK-048：编写产品 README、示例与迁移指南

- **依赖**：SDK-030、SDK-039、SDK-045、SDK-047。
- **唯一目标**：让外部 Go 项目从安装到第一次 Run 有唯一推荐路径。
- **设计**：提供 quickstart、生命周期、Run、stream、cancel、renew、errors、retry、安全、cleanup 和旧 API migration。
- **修改范围**：SDK README、examples、根 README 入口和 docs-dev 指南。
- **测试**：所有代码块 compile/run；链接检查。
- **验收**：用户无需阅读源码或复制 Bash HTTP 请求即可使用 SDK。
- **本任务不做**：不承诺未实现的 files/PTY/port。

### SDK-049：建立 release dry-run 与 SemVer CI

- **依赖**：SDK-001、SDK-006、SDK-046、SDK-048。
- **唯一目标**：证明 SDK 可作为独立 Go module 被外部项目消费。
- **设计**：校验 clean go.mod/go.sum、tag 规则、license、module zip、API diff、临时 consumer build；生成 changelog 草案。
- **修改范围**：release script/CI 与 version policy。
- **测试**：禁用 go.work、本地 replace 和 module cache 后执行 consumer test。
- **验收**：产物不含本机路径、secret、测试数据库或服务端二进制。
- **本任务不做**：未经用户授权不创建远程 tag 或发布 release。

### SDK-050：执行最终验收并对齐 Phase 4

- **依赖**：SDK-000～SDK-049。
- **唯一目标**：形成 SDK Phase 最终报告，并让 Phase 4 复用本阶段架构。
- **设计**：记录最终 SHA、module/version、API snapshot、测试矩阵、Linux Docker 证据、已知限制；调整 P4-096～P4-103 的重复职责描述。
- **修改范围**：SDK acceptance report、Phase 4 计划链接与状态，不再修改产品代码。
- **测试**：完整 SDK/root tests、race、contract、release dry-run、真实 SDK E2E。
- **验收**：所有门禁和任务 PASS，工作树干净，Phase 4 不再重新实现 lifecycle/execution facade。
- **本任务不做**：不开始 Phase 4 files/PTY/port 开发。

## 10. 测试矩阵

| 能力 | Unit | Contract | Race | Fuzz | Live sandboxd |
|---|---:|---:|---:|---:|---:|
| Client/URL/options | 是 | 否 | 是 | 是 | 否 |
| JSON/error decoding | 是 | 是 | 否 | 是 | 否 |
| Lifecycle | 是 | 是 | 是 | 否 | 是 |
| Background execution | 是 | 是 | 是 | 否 | 是 |
| SSE execution | 是 | 是 | 是 | 是 | 是 |
| Wait/poll | 是 | 否 | 是 | 否 | 是 |
| Log iterator/collector | 是 | 是 | 是 | 是 | 是 |
| Run | 是 | 是 | 是 | 否 | 是 |
| Retry | 是 | 是 | 是 | 否 | 可选故障注入 |
| Hooks/redaction | 是 | 否 | 是 | 是 | 否 |
| Module/release | 是 | 否 | 否 | 否 | 外部 consumer build |

真实验收至少覆盖：

```text
server readiness
  -> create with idempotency key
  -> WaitRunning
  -> Run stdout/stderr/exit 0
  -> Run non-zero exit
  -> ExecuteStream
  -> start + cancel + wait
  -> renew equal/extend/reject shorten
  -> DeleteAndWait + repeated delete
```

## 11. Commit 与审查约定

每个 `SDK-NNN` 必须独立提交。建议 commit message：

```text
docs(sdk): freeze public API contract
build(sdk): create independent Go module
feat(sdk): add sandbox wait helper
feat(sdk): add bounded run output collector
fix(sdk): prevent retrying execution start
test(sdk): add live product workflow acceptance
```

审查优先级：

1. 是否可能重复创建 sandbox 或重复启动命令；
2. 是否泄露 token、命令、env、输出、ID 或内部 endpoint；
3. context、body、timer、iterator 和 goroutine 是否释放；
4. SDK public model 是否重新耦合 protocol/internal；
5. retry/wait/cleanup 是否符合冻结矩阵；
6. API 是否比底层 HTTP 更易用且没有隐藏危险行为；
7. 独立 module、版本和示例是否真实可消费；
8. Phase 4 是否能够增量扩展而不是重写。

## 12. 阶段完成后的能力与限制

完成本阶段后，Go 用户可以：

- 通过标准 module path 引入 SDK；
- 使用资源句柄管理 sandbox 与 execution；
- 一次 `Run` 获得 stdout、stderr、exit code 和 typed error；
- 使用 iterator 消费后台日志或前台 SSE；
- 使用 context、Wait、CancelAndWait、DeleteAndWait 控制生命周期；
- 安全使用幂等 create、绝对时间 renew 和有界 retry；
- 通过稳定错误和 request ID 做程序化诊断；
- 在不依赖 Docker SDK 或 MiniSandbox 内部包的情况下编译应用。

仍然不支持：

- files、PTY、port proxy 和 capabilities；
- TypeScript/Python SDK；
- execution 跨 runner/container 重启恢复；
- Pool、快照、分布式调度和更强隔离 runtime；
- SDK 自动启动或运维服务端。

这些限制与 Phase 4、Phase 5 的阶段边界保持一致。
