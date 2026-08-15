# ADR-0004：Phase 4 依赖选型

- 状态：已接受（P4-001）
- 日期：2026-08-15
- 背景：Phase 4 需要 Go 的 WebSocket 与 PTY 能力，以及 TypeScript 和 Python SDK。

## 决定

| 语言/用途 | 依赖 | 版本 | 理由 |
|---|---|---|---|
| Go WebSocket | `github.com/coder/websocket` | v1.8.15 | 维护活跃、零传递依赖；支持 subprotocol、消息上限、context 和自定义 `http.Client` 拨号（Unix Socket 必需）；服务端 `Accept` 与客户端 `Dial` 都需要（runner ↔ sandboxd bridge） |
| Go PTY | `github.com/creack/pty` | v1.1.24 | 事实标准；只用 `pty.Open()` 与 `pty.Setsize()`，进程启动沿用 runner 自己的 `exec` 路径以保留非 root 身份与进程组语义 |
| fd-relative syscall | `golang.org/x/sys/unix`（已有） | v0.47.0 | `Openat2`、`Renameat2`、`Unlinkat` 等；openat2 不可用时 files 能力明确报不可用 |
| TypeScript SDK | 无运行时依赖 | — | 仅用 Node 原生 `fetch` 与 `WebSocket`，要求 Node ≥ 22（验收用 Node 24）；开发依赖仅 `typescript`、`@types/node` |
| Python SDK | 无依赖（纯标准库） | — | `http.client`/`urllib` 做 HTTP/SSE；PTY 的 WebSocket 客户端按 RFC 6455 最小实现（仅 client、无扩展），保持零安装成本 |

## 约束

- 不自行实现服务端 RFC 6455；Go 侧一律使用 `coder/websocket`。
- Python 的手写 WS 客户端只连接本服务的 `minisandbox.pty.v1`，不作为通用 WS 库。
- 依赖文件在本任务集中更新；版本进入代码后由 `go mod tidy` 转为直接依赖。
