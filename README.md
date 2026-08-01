# MiniSandbox

MiniSandbox 是一个全 Go 实现的 Agent sandbox(沙盒)运行时,目标是让 AI Agent
在受控的 Docker 容器中安全地执行命令。整体架构来自
[《全 Go Agent Sandbox Runtime 设计》](docs/all-go-agent-sandbox-runtime-design.md),
并以本地只读参考仓库 `OpenSandbox/` 的分层经验为蓝本做了 Docker-first 裁剪
(参见[分析文档](docs/opensandbox-sandbox-module-and-go-runtime-analysis.md))。

> **当前状态**:Phase 1 Docker 生命周期已经完成。`sandboxd` 支持异步创建、
> 查询、幂等删除、失败补偿和重启恢复，并由真实 Linux Docker 集成测试保护。
> Phase 1 不提供用户命令执行；runner 的执行接口仍明确返回
> `501 NOT_IMPLEMENTED`。运行步骤见
> [Phase 1 Docker 生命周期指南](docs/getting-started/phase1-docker-lifecycle.md)。

## 架构设计

本节是概览;架构与安全边界的单一事实源是
[总体设计文档](docs/all-go-agent-sandbox-runtime-design.md)。

### 三进程模型

系统由三个进程组成,生命周期控制面与容器内执行数据面严格分进程:

```text
 客户端 / sdk/go
     │  HTTP(默认仅 127.0.0.1:8080,Phase 1 不作为公网服务)
     ▼
 ┌─ 宿主机 ─────────────────────────────────────────────
 │
 │  sandboxd —— 生命周期控制面
 │    api → application → domain
 │               │            │
 │          store 端口    runtime 端口      reconcile:
 │               │            │            期望状态 ⇄ 实际状态收敛
 │               ▼            ▼
 │           SQLite      Docker Engine
 │         (期望状态)  (实际状态事实源)
 │                            │  创建 volume/容器、写 labels、
 │                            │  注入 go:embed 的静态二进制
 │                            ▼
 │  ┌─ sandbox 容器(每个 sandbox 一个)─────────────
 │  │
 │  │  sandbox-init(PID 1:信号转发、孤儿回收)
 │  │    └── runnerd(命令执行数据面)
 │  │          ▲
 │  └──────────┼──────────────────────────────────────
 │             │  每个 sandbox 独立的 Unix Socket,
 │             │  不发布任何 TCP 端口
 │  sandboxd ──┘(runnerclient,固定端点白名单)
 └──────────────────────────────────────────────────────
```

- **`sandboxd`**:宿主机控制面。提供公共生命周期 HTTP API,持久化期望状态,
  通过 Docker Engine 收敛实际状态,重启后依据 labels 恢复受管资源。
  永远不在宿主机直接执行用户命令。
- **`sandbox-init`**:容器 PID 1。只负责启动 runnerd、向子进程组转发终止信号、
  回收孤儿进程并传播退出码,不承载 HTTP API 或业务逻辑。
- **`runnerd`**:容器内数据面。只通过当前 sandbox 的 Unix Socket 服务,
  不能访问 Docker socket,也没有管理其他 sandbox 的标识或接口。

### 核心机制(设计)

1. **期望状态 + reconcile**:API 只提交意图(创建即写入 `desired=Running`,
   删除即改写 `desired=Terminated`)并立即返回 `202`;后台 reconciler 幂等地把
   Docker 实际状态收敛到期望状态。SQLite 保存期望状态与元数据,Docker 是实际
   状态的事实源,`minisandbox.io/*` labels 是重启恢复协议的一部分。
2. **二进制嵌入与注入**:`runnerd` 和 `sandbox-init` 以
   `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 静态构建,经 `go:embed` 打进
   `sandboxd` 单一二进制;创建容器时通过 Docker Copy API 注入容器内
   `/opt/minisandbox/`,entrypoint 固定为 `sandbox-init -- runnerd serve`,
   因此任意用户镜像无需预装任何组件。
3. **两阶段就绪**:"容器已启动"不等于"Agent 可用"。sandboxd 经 Unix Socket
   探测 runnerd 的 `/healthz` 成功后,sandbox 才进入 `Running`。
4. **Unix Socket 数据面**:runner 流量走宿主机
   `/var/lib/minisandbox/run/<id>/runner.sock`(目录 0700)与容器内
   `/run/minisandbox/runner.sock` 的挂载映射,不发布任何 TCP 端口;
   sandboxd 只代理固定的 runner 端点白名单。
5. **状态语义**:对外状态为 6 值枚举
   `Pending/Creating/Running/Stopping/Terminated/Failed`,搭配 17 个稳定的
   机器可读 `reason` 和不含秘密、宿主机路径与内部堆栈的安全 `message`。

### 生命周期流程(Phase 1 目标闭环)

```text
POST /v1/sandboxes ──────────── 202 Accepted(state=Pending)
  → Store 持久化 desired=Running / observed=Pending
  → reconciler:校验并拉取镜像 → 创建 workspace volume
      → 以停止态创建容器(安全配置 + labels)→ 注入 init/runnerd → 启动容器
      → Unix Socket 探测 runnerd /healthz → observed=Running
GET /v1/sandboxes/{id} ───────── 随时查询 state / reason / message
DELETE /v1/sandboxes/{id} ────── 202,仅置 desired=Terminated(重复删除幂等)
  → reconciler 清理容器、非持久 volume 和 runtime 目录 → observed=Terminated
```

`sandboxd` 重启后扫描 Store 与带 `minisandbox.io/managed=true` label 的容器,
重新建立关联;创建中途失败会进入带原因的 `Failed` 状态并补偿清理,不遗留
无人管理的资源。

### 分层与依赖方向

```text
api -> application -> domain
                   -> runtime 端口
                   -> store 端口

runtime/docker -> runtime 端口 + domain
store/sqlite   -> store 端口 + domain
runnerclient   -> pkg/protocol
runner         -> pkg/protocol
sdk/go         -> pkg/protocol
```

`internal/domain` 不依赖 HTTP、Docker SDK 或 SQLite driver;`pkg/protocol`
只承载稳定 wire model,不依赖任何 `internal` 包。完整的开发规则与安全不变量
见 [AGENTS.md](AGENTS.md)。

### 安全边界(设计不变量)

- `sandboxd` 不在宿主机执行用户命令;`runnerd` 接触不到 Docker socket。
- 容器默认配置:非 privileged、`CapDrop=ALL`(仅回加
  `CHOWN/SETUID/SETGID/KILL`)、`NoNewPrivileges`、默认 seccomp、
  CPU/内存/进程数限额;禁止 host network/PID/IPC、任意设备与任意宿主机挂载。
- 用户命令以容器内非 root 身份执行(Phase 2 实现身份切换)。
- 秘密不写入 Docker labels、日志、错误响应或可被用户命令继承的环境。
- 取消与超时终止完整进程组(SIGTERM → 宽限期 → SIGKILL),不只杀主进程。

## 模块说明

状态标记:**已实现** = 当前范围内功能完整;**骨架** = 结构就绪、核心逻辑返回
501/`ErrNotImplemented` 或尚未接线;**仅文档** = 只有意图说明。契约、配置与
文档等非代码条目按其自身成熟度标注。

### 进程入口(`cmd/`)

| 模块 | 职责 | 当前状态 |
|---|---|---|
| `cmd/sandboxd/` | 控制面入口:装配 HTTP API、store、runtime、reconcile 与优雅关闭 | 已实现(Phase 1 生命周期范围) |
| `cmd/runnerd/` | 数据面入口:`serve` 子命令,`-socket`(默认 `/run/minisandbox/runner.sock`,目录 0700、socket 0600),token 取自 `MINISANDBOX_RUNNER_TOKEN` | 骨架:socket 服务、鉴权与优雅关闭已实现,执行端点 501 |
| `cmd/sandbox-init/` | 容器 PID 1:`sandbox-init -- command [args...]` | 骨架:子进程组启动、信号转发、退出码传播已实现;通用孤儿回收未实现;Windows 侧为仅保证编译的占位 |

### 控制面(`internal/`)

| 模块 | 职责 | 当前状态 |
|---|---|---|
| `internal/api/` | HTTP 适配层:路由、request-ID 中间件、响应编码与错误映射;业务规则必须委托 application | 已实现(Phase 1 health/ready/create/get/delete) |
| `internal/application/` | 用例编排:连接 domain、store 端口与 runtime 端口 | 已实现(Phase 1 生命周期用例) |
| `internal/domain/` | 领域模型:`Sandbox`、resolved `SandboxSpec`、状态/期望状态枚举、领域错误、执行规格 | 已实现(Phase 1 范围),测试最完整的包 |
| `internal/runtime/` | Runtime 端口:`Ensure/Inspect/Delete/ListManaged` 接口与观测状态类型 | 已实现(纯接口定义) |
| `internal/runtime/docker/` | Docker adapter:镜像、容器、workspace、labels、注入;不负责租户鉴权与配额 | 已实现(Phase 1 ensure/inspect/delete/list) |
| `internal/store/` | Store 端口:`Save/Get/List/Delete` 持久化接口 | 已实现(纯接口定义) |
| `internal/store/sqlite/` | SQLite adapter:schema 迁移与事务存取 | 已实现(Phase 1 schema 与 CAS 持久化) |
| `internal/reconcile/` | 收敛层:启动恢复、唤醒队列、按 sandbox 串行化(keyed lock)、幂等收敛 | 已实现(Phase 1 创建、删除、恢复和 worker) |
| `internal/embedded/` | 读取注入容器的静态构建产物(`go:embed`) | 已实现;产物由 `make build` 生成,全新 checkout 中只有占位 README |

### 数据面与协议

| 模块 | 职责 | 当前状态 |
|---|---|---|
| `internal/runner/` | runnerd 服务实现:鉴权、进程组、输出、取消与后台任务 | 骨架:server/常量时间 TokenAuth/优雅关闭/执行注册表/输出缓冲/进程组工具已实现,执行端点 501 |
| `internal/runnerclient/` | sandboxd → runnerd 的 Unix Socket 客户端与 SSE 解码 | 骨架:`Health` 与 `DecodeSSE` 已实现,尚无 Execute/Cancel 方法;Windows transport 为恒定失败占位 |
| `pkg/protocol/` | 稳定 HTTP/SSE wire model:状态与 reason 枚举、请求/响应、错误 envelope、执行事件 | 已实现,字段与枚举被测试冻结 |
| `sdk/go/` | 面向用户的 Go 客户端:`CreateSandbox/GetSandbox/DeleteSandbox` 与类型化错误 | 已实现(Phase 1 生命周期范围) |

### 契约、测试、配置与文档

| 模块 | 职责 | 当前状态 |
|---|---|---|
| `api/lifecycle.openapi.yaml` | 冻结的 Phase 1 公共生命周期契约(health/ready/create/get/delete,统一错误 envelope) | 已冻结,由契约测试保护 |
| `api/runner.openapi.yaml` | sandboxd ↔ runnerd 内部契约(healthz、SSE 执行、取消) | 初稿 |
| `tests/contract/` | 冻结契约表面与 wire fixture 的离线测试 | 已实现(`tests/` 下唯一有真实测试的套件) |
| `tests/integration/` | 面向真实 Docker 的生命周期与安全测试(opt-in,须精确清理) | 已实现(Phase 1) |
| `tests/security/` | 后续集中安全套件入口 | 仅文档；Phase 1 安全断言当前位于各包单测和 `tests/integration/` |
| `configs/sandboxd.example.yaml` | 示例配置:监听地址、数据目录、Docker host、reconcile 间隔、TTL 限额 | 已实现并与配置默认值同步 |
| `docs/` | 总体设计、OpenSandbox 分析、Phase 1–4 开发计划、依赖 ADR | 最完整的部分;各阶段计划待审查 |
| `OpenSandbox/` | 上游参考仓库的本地只读 checkout | 不属于本仓库提交内容(已 gitignore) |

## 快速开始

```bash
go test ./...
```

完整的 Linux 构建、启动、create/get/delete 和定向清理命令见
[Phase 1 Docker 生命周期指南](docs/getting-started/phase1-docker-lifecycle.md)；
阶段测试环境与证据见
[Phase 1 验收报告](docs/reports/phase1-acceptance.md)。

## 构建 Linux 产物

```bash
make build
```

`make build` 按固定顺序执行:先把 `runnerd` 和 `sandbox-init` 以静态
linux/amd64 构建到 `internal/embedded/artifacts/linux_amd64/`,再编译
`sandboxd`(此时 `go:embed` 把两个产物打进控制面二进制),最终把三个二进制
写入 `bin/`。另有 `make test / fmt / vet / clean`。

`Dockerfile` 构建的是 **sandboxd 控制面镜像**(两阶段,最终为 distroless
non-root),不是 sandbox 容器镜像——sandbox 使用任意用户镜像,由 sandboxd
注入组件。

### 在 Windows 上开发

仓库带有 `//go:build windows` 占位实现,保证在 Windows 开发机上可以编译和
运行单元测试;但 sandbox 运行时本身只面向 Linux + Docker Engine,信号、
进程组与 Unix Socket 相关能力需在 Linux(如 WSL2)中构建与验证。

## 开发路线图

| 阶段 | 内容 | 计划 |
|---|---|---|
| Phase 1 | Docker 生命周期:持久化、异步创建、注入启动、健康探测、幂等删除、重启恢复 | [phase-1 计划](docs/phase-1-docker-lifecycle-development-plan.md)(已完成) |
| Phase 2 | PID 1 语义、非 root 执行、argv/shell、SSE、超时/取消、进程组、受控出站网络 | [phase-2 计划](docs/phase-2-runner-execution-development-plan.md) |
| Phase 3 | TTL 与续期、幂等创建、周期对账、崩溃恢复、孤儿处理、指标诊断 | [phase-3 计划](docs/phase-3-reliability-development-plan.md) |
| Phase 4 | 工作区文件 API、PTY、端口代理、Go/TS/Python SDK、镜像预拉取 | [phase-4 计划](docs/phase-4-agent-experience-development-plan.md) |

更远期的 Kubernetes、Pool 与强隔离(gVisor/Kata)方向见总体设计文档。

## 模块路径

在仓库尚未配置远程时,module path 有意使用本地名 `minisandbox`;发布前请修改:

```bash
go mod edit -module example.com/your-org/mini-sandbox
```
