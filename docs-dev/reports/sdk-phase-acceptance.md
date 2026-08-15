# SDK Phase 最终验收报告（SDK-025）

## 1. 结论

**SDK Phase（SDK-000～SDK-025）全部 26 个任务完成，阶段可以结束。**

- Windows 全量 `go test ./...`、`go vet ./...`、`gofmt` 检查通过；
- WSL2（Ubuntu 24.04，原生 Docker Engine 29.1.3）真实服务端验收
  `go run ./tests/sdk` 取得 **10/10 PASS**；
- 验收后受管容器、卷与临时数据目录全部清理，无残留。

本报告提交前的最后一个代码提交为
`4d6be79e0b253253b21907a254a52a3a45e56dcc`（验收清单同步）；按仓库惯例，
报告不在自身内容中记录自身 commit，最终 SHA 从 Git 历史读取。

## 2. 任务完成状态

| 阶段 | 任务 | 状态 | 关键提交 |
|---|---|---|---|
| A | SDK-000 基线确认 | 完成 | `41f7434` |
| A | SDK-001 推荐流程与方法列表 | 完成 | `41f7434` |
| A | SDK-002 状态/事件类型公开 | 完成 | `847b35f` |
| A | SDK-003 信息与结果类型 | 完成 | `4004cc7` |
| A | SDK-004 错误 helper 与终态错误 | 完成 | `6bdced5` |
| A | SDK-005 基础接口验收 | 完成 | `93180ca` |
| B | SDK-006 Sandbox 资源对象 | 完成 | `9a2efad` |
| B | SDK-007 Client.Create | 完成 | `1bd25b6` |
| B | SDK-008 Sandbox.Info | 完成 | `45a72d2` |
| B | SDK-009 Sandbox.WaitRunning | 完成 | `027bf63` |
| B | SDK-010 Renew/Delete/DeleteAndWait | 完成 | `7fa7575` |
| B | SDK-011 生命周期验收 | 完成 | `1ca97af` |
| C | SDK-012 Execution 资源对象 | 完成 | `68acfa4` |
| C | SDK-013 StartExecution 与 Info | 完成 | `d596c84` |
| C | SDK-014 Wait 与 CancelAndWait | 完成 | `96e50e0` |
| C | SDK-015 日志迭代器 | 完成 | `859a630` |
| C | SDK-016 Sandbox.Run | 完成 | `b07b32c` |
| C | SDK-017 ExecuteStream（SSE） | 完成 | `495556e` |
| C | SDK-018 Execution/Run/SSE 验收 | 完成 | `b095686` |
| D | SDK-019 Health | 完成 | `bd1b77b` |
| D | SDK-020 Readiness | 完成 | `e34c295` |
| D | SDK-021 Quickstart | 完成 | `bd75758` |
| D | SDK-022 验收程序高层化 | 完成 | `2f07b76` |
| D | SDK-023 真实服务端验收 | 完成 | 本报告第 4 节 |
| E | SDK-024 SDK 使用指南 | 完成 | `bd90ec4` |
| E | SDK-025 最终回归与报告 | 完成 | 本报告 |

计划第 8 节的完成标准逐项核对：普通用户只导入 `sdk/go` 即可完成核心工作流；
创建即得资源对象；sandbox/execution 等待无需手写轮询；日志无 cursor/Base64；
`Run` 一次取得 stdout/stderr/退出码/状态；SSE 边执行边读；health/readiness
可查；显式续期/取消/删除；README 提供可运行推荐路径；`tests/sdk` 使用高层
API 且真实验收通过；9 个底层方法全部保留。

## 3. 最终能力

新增公开 API（全部带中文 doc 注释）：

- `Client`：`Create`（variadic `CreateOption`，含 `WithIdempotencyKey`）、
  `Sandbox(id)`、`Health`、`Readiness`；
- `Sandbox`：`ID`、`Info`、`WaitRunning`、`Renew`、`Delete`、`DeleteAndWait`、
  `StartExecution`、`Run`、`ExecuteStream`；
- `Execution`：`ID`、`Info`、`Wait`、`CancelAndWait`、`Logs`（已解码迭代器）；
- `EventStream`：前台 SSE 事件迭代器（`Next`/`Event`/`Err`/`Close`）；
- 类型：`SandboxInfo`、`ExecutionInfo`、`DecodedEvent`（已解码 `Data`；
  wire 别名 `ExecutionEvent` 原样保留）、`RunResult`、`Readiness`、
  `ReadinessComponent`，以及状态/事件枚举常量；
- 错误：`ResponseError`（新增 `IsNotFound`/`IsConflict`/`IsRetryable`）、
  `ExitError`、`ExecutionCancelledError`、`ExecutionTimedOutError`、
  `ExecutionFailedError`。

SSE 解码复用与控制面 runnerclient 一致的 64KiB 行 / 128KiB 帧缓冲上限，
校验 event 字段与载荷一致、序号连续且 execution ID 不中途变化。

## 4. 验证证据

| 检查 | 环境 | 结果 |
|---|---|---|
| `gofmt -l`（sdk/、tests/sdk/） | Windows | 无输出 |
| `go test ./...`（全仓库） | Windows（go1.26.4） | 全部 PASS |
| `go vet ./...`（全仓库） | Windows | 无输出 |
| `go test ./sdk/go/...` | Windows | PASS（含新增 5 组验收测试） |
| `make build`（sandboxd/runnerd/sandbox-init + 嵌入产物） | WSL2 Ubuntu 24.04 / go1.26.0 | PASS |
| `sandboxd` 启动 + `/readyz` 五组件就绪 | WSL2 + 原生 Docker 29.1.3 | PASS |
| `go run ./tests/sdk` | WSL2 真实服务端 | **10/10 PASS** |
| 受管容器/卷/network（MiniSandbox labels） | WSL2 验收后审计 | 0 残留 |
| `/tmp/sdk-acc-*` 临时目录与 sandboxd 进程 | WSL2 验收后审计 | 已清理 |

真实验收 10 项：S10 health/readiness 预检、S01 创建等待 Running、S02 幂等
重放与 409 冲突、S03 后台执行与自动解码日志、S04 取消长任务、S05 续期
（含重放与拒绝缩短）、S08 Run（含 exit 7 的 `ExitError`）、S09 前台 SSE、
S06 错误模型与 Context、S07 删除收敛与重复删除。

## 5. 过程中的修复

- `tests/contract/phase2_documentation_test.go` 在阶段开始前即失败：文档目录
  重组（`docs/` → 未跟踪的 `docs-dev/`）后，该测试仍读取已不存在的
  `docs/phase-2-operations-guide.md`。本阶段以
  `a798f48` 将其收敛为“示例配置与宣传默认值一致”的仓库内契约，全量测试
  因此恢复绿色。该修复独立提交，未混入 SDK 变更。

## 6. 已知限制与环境说明

1. **WSL 单测环境限制**：本机 WSL2（Docker Desktop mirrored networking，
   存在 `loopback0` 接口）中，进程内 `httptest` 监听的临时端口在 loopback
   上连接被拒绝（最小复现：`net.Listen("127.0.0.1:0")` 成功但 dial 被拒；
   固定端口如 8080/8081 正常）。该问题同等影响阶段开始前就存在的
   `sdk/go`、`internal/runnerclient` 等 httptest 单测，与本阶段代码无关。
   因此沿 Phase 3 先例：单测以 Windows 全量为门槛，Linux 侧以真实服务端
   验收为门槛。
2. **需要 root 的单测**：`internal/runner` 的 socket 属主测试需要 root 才能
   chown；以 root 运行时通过。另一个 Linux identity 测试
   `TestVerifyRestrictedIdentityRootHelper`（exit status 14）为阶段开始前
   已有行为，从未纳入单测门槛，不属于本阶段范围。
3. **SDK 设计边界**（计划第 3.3 节维持不变）：无自动重试、无自动续租守护、
   无 files/PTY/port proxy（Phase 4 范围）、无独立 module 与发布流水线。
4. **等待方法的轮询节奏**：内置 250ms 间隔，不暴露配置；总时长由调用方
   context deadline 控制。
5. **验收程序编号**：S01～S07 保留原编号与语义（改用高层 API），新增
   S08（Run）、S09（SSE）、S10（health/readiness，最先执行）；打印顺序为
   S10、S01～S05、S08、S09、S06、S07，全部通过输出 `10/10 PASS`。

## 7. 收尾判定

| 门槛 | 结果 |
|---|---|
| 26 个任务全部完成并独立提交 | PASS |
| Windows 全量 `go test ./...` / `go vet ./...` / `gofmt` | PASS |
| 真实 Linux + Docker SDK 验收 10/10 | PASS |
| 底层 API 与公共契约零破坏（无 OpenAPI/protocol 变更） | PASS |
| README / SDK 指南 / 验收清单 / 示例同步 | PASS |
| 验收后无受管资源残留 | PASS |

**最终判断：SDK Phase 完成，可进入 Phase 4（Agent 体验）。**
