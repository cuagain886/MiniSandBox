# Phase 4 启动前验收清单（P4-000）

## 1. 结论

**P4-000 状态：PASS。可以开始 P4-001。**

截至 2026-08-15（验证基线 commit `1529cb3`），Phase 1～3 与 SDK Phase 的交付物、
验收证据和当前代码状态均已核对，现有服务能够创建 sandbox 并执行命令，可以作为
Phase 4 的开发基础。

## 2. 前置阶段证据

| 前置 | 证据 | 判断 |
|---|---|---|
| Phase 1 | `docs-dev/reports/phase1-acceptance.md`（PASS） | Docker 生命周期闭环 |
| Phase 2 | `docs-dev/reports/phase2-acceptance.md`（13/13 PASS） | runner 执行、取消、超时 |
| Phase 3 | `docs-dev/reports/phase3-kickoff-checklist.md`（P3-000 PASS）+ 仓库 integration/security 测试 | 可靠性、恢复、TTL、幂等 |
| SDK Phase | `docs-dev/reports/sdk-phase-acceptance.md`（10/10 真实 PASS + 评审修复） | 高层 Go SDK |

Phase 3 的收尾以 kickoff 清单中引用的真实 integration/security 回归与本仓库
`tests/integration`、`tests/security` 测试为证据；独立成篇的 phase3-acceptance
未单列，不构成 Phase 4 阻塞。

## 3. 当前基线检查（验证于 `1529cb3`）

| 检查 | 结果 |
|---|---|
| Windows `go test ./...` | 27 个包全部 PASS，无 FAIL |
| `go vet ./...` | PASS（SDK Phase 收尾时确认） |
| 真实 WSL2 + 原生 Docker SDK 验收 | 10/10 PASS（评审修复后复跑） |
| 受管资源残留 | 0 容器 / 0 卷（验收后审计） |
| 工作树 | 仅用户自己的 SDK 计划文档本地修改，与本阶段无关 |

## 4. Phase 4 开发环境

| 项目 | 值 | 用途 |
|---|---|---|
| Windows Go | go1.26.4 windows/amd64 | 单元测试门槛 |
| WSL2 Ubuntu 24.04 Go | go1.26.0 linux/amd64 | Linux 构建、真实验收 |
| Docker | 原生 dockerd `unix:///run/minisandbox-native-docker.sock`（29.1.3） | 真实验收 |
| Node.js（Windows） | v24.14.0 + npm 11.9.0 | TypeScript SDK 构建与测试 |
| Python（WSL） | 3.12.3 | Python SDK 测试 |
| `golang.org/x/sys` | v0.47.0（已在 go.mod） | openat2 等 fd-relative syscall |

## 5. 已知环境限制（沿用并扩展 SDK Phase 结论）

1. WSL 中 httptest 临时端口的 loopback 连接被拒（Docker Desktop mirrored
   networking 环境问题）：单测以 Windows 为门槛，Linux 侧以真实 Docker 验收
   为门槛；
2. `internal/runner` 个别测试需要 root 或特定 Linux 身份条件：需要时在
   WSL root 会话运行，不纳入常规门槛；
3. TypeScript SDK 在 Windows Node 24 下开发测试（原生 fetch/WebSocket 可用，
   不引入运行时 npm 依赖）；Python SDK 以 WSL Python 3.12 + 标准库优先。

## 6. Phase 4 启动判定

| 门槛 | 结果 |
|---|---|
| Phase 1～3 与 SDK Phase 交付物可追溯 | PASS |
| 当前基线全量测试绿色 | PASS |
| 真实 Docker 环境可用 | PASS |
| 三语言工具链可用 | PASS |
| 无未解释的资源残留或失败测试 | PASS |

**判定：Phase 4 前置条件满足，下一任务为 P4-001（确定依赖）。**
