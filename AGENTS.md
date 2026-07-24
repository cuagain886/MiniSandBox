# MiniSandbox AGENTS

本文件是仓库级开发规则。任务目录下若以后出现更近的 `AGENTS.md`，优先遵守更近的文件；没有局部规则时，再参考相邻的 `README.md`、设计文档、测试和 CI 配置。

这些规则提炼自 OpenSandbox 的分层开发约束，并按当前全 Go、Docker-first 的 MiniSandbox 设计做了裁剪。

## 仓库地图

| 路径 | 职责 |
|---|---|
| `cmd/sandboxd/` | 宿主机生命周期控制面入口 |
| `cmd/runnerd/` | sandbox 容器内执行数据面入口 |
| `cmd/sandbox-init/` | 容器 PID 1、信号转发与孤儿回收入口 |
| `api/` | 对外生命周期 API 和内部 runner API 的 OpenAPI 契约 |
| `internal/api/` | HTTP decode、鉴权、中间件和错误映射 |
| `internal/application/` | 生命周期与执行用例编排 |
| `internal/domain/` | 不依赖 HTTP、Docker、SQLite 的领域模型 |
| `internal/runtime/` | runtime 接口及 Docker adapter |
| `internal/store/` | 持久化接口及 SQLite adapter |
| `internal/reconcile/` | 期望状态收敛、调度、TTL 与 keyed lock |
| `internal/runner/` | 命令执行、进程组、输出、取消与后台任务 |
| `internal/runnerclient/` | `sandboxd` 到 `runnerd` 的 Unix Socket 客户端 |
| `internal/embedded/` | 注入容器的静态 runner/init 构建产物 |
| `pkg/protocol/` | 稳定的 HTTP/SSE wire model |
| `sdk/go/` | 面向用户的 Go SDK |
| `tests/` | contract、integration 和 security 测试 |
| `configs/` | 可运行的示例配置 |
| `docs/` | 架构、设计与长篇说明 |
| `OpenSandbox/` | 只读参考源码，不属于本仓库提交内容 |

## 单一事实源

不同内容必须有明确的 source of truth：

| 内容 | Source of truth | 同步要求 |
|---|---|---|
| 公共 HTTP/SSE 契约 | `api/*.openapi.yaml` | 同步 handler、`pkg/protocol`、SDK、文档和 contract tests |
| 架构与安全边界 | `docs/all-go-agent-sandbox-runtime-design.md` | 同步受影响的接口、实现、配置和 security tests |
| 领域状态与不变量 | `internal/domain/` | wire model 通过显式转换适配，不把领域对象直接当 API DTO |
| 配置字段和默认值 | 配置模型代码 | 同步 `configs/sandboxd.example.yaml` 和用户文档 |
| Docker labels | `internal/runtime/docker/labels.go` | 同步创建、发现、恢复、清理逻辑和测试 |
| 嵌入式 Linux 二进制 | `cmd/runnerd/`、`cmd/sandbox-init/` | 通过构建流程生成，不能直接修改二进制解决问题 |

公共接口优先采用向后兼容的增量修改。修改 OpenAPI 后，应在同一变更中更新所有实际受影响的消费者；无法验证的消费者必须在交付说明中明确列出。

## 工作原则

- 编码前先确认任务目标、作用域、假设和可验证的完成标准。
- 实现满足需求的最小方案，避免提前加入 Pool、快照、Kubernetes、PTY 或一次性抽象。
- 只修改完成当前任务所需的文件，不顺手重构或删除无关代码。
- 优先沿用现有包边界、命名、错误语义和测试模式。
- 行为变化或 bug 修复必须添加聚焦测试；先运行包级检查，再扩大到全仓库。
- 不掩盖未实现能力。占位实现应返回明确错误，不能伪装成功。

## 中文注释规范

- 每个 Go package 都必须在模块主要源文件的 `package` 声明前提供中文包注释，说明模块功能、主要职责和不能承担的职责；不要仅为包注释新增 `doc.go`。
- 每个导出的类型、函数、方法、变量和常量组都必须有中文 Go doc 注释，并以被注释的标识符开头。
- 接口方法、状态值、协议字段的注释要说明调用语义、单位、幂等性或兼容性要求，不能只把英文名称直译一遍。
- 安全边界、reconcile 状态转换、进程组终止、资源清理和跨层映射等不直观逻辑必须添加中文原因注释，重点解释“为什么”。
- 注释必须与代码同步更新；禁止保留描述旧行为的失真注释，也不要为显而易见的赋值和流程逐行堆砌注释。
- 新增 package 或导出 API 时，中文注释属于完成标准；缺少对应注释的变更不得视为完成。

## 架构边界

以下约束不是实现偏好，而是安全不变量：

- 生命周期控制面和容器内执行数据面保持分进程。
- `internal/api` 的 handler 保持轻薄；业务规则放在 `application`，运行时细节放在 adapter。
- `internal/domain` 不得 import HTTP、Docker SDK 或 SQLite driver。
- `sandboxd` 不得通过 `os/exec`、shell 或等价方式在宿主机执行用户命令。
- `runnerd` 不得访问 Docker socket，也不得接受管理其他 sandbox 的标识或接口。
- `runnerd` 默认只监听当前 sandbox 的 Unix Socket，不新增公网或宿主机 TCP 端口。
- 创建、续期和删除采用“期望状态写入 + reconcile”，reconcile 必须可重复调用且幂等。
- 命令取消和超时必须终止完整进程组，不能只杀 shell 主进程。
- runner token、用户环境变量、凭据、命令和输出不得写入 Docker labels。
- 机密不得出现在日志、错误响应、命令行参数或可由普通命令继承的 runner 配置环境中。
- 默认执行身份为容器内非 root 用户；不得为 nested sandbox、挂载或网络功能随意增加 capabilities、privileged 或 Docker socket。
- 第一版依赖容器边界。引入 nested bubblewrap、gVisor、Kata 或 microVM 前必须先更新威胁模型和设计文档。

## 分层和依赖

依赖方向保持为：

```text
api -> application -> domain
                   -> runtime interface
                   -> store interface

runtime/docker -> runtime interface + domain
store/sqlite   -> store interface + domain
runnerclient   -> protocol
runner         -> protocol
sdk/go         -> protocol
```

具体要求：

- handler 不直接 import Docker adapter 或 SQLite adapter。
- Docker adapter 不决定租户、鉴权、配额或 API 错误码。
- runner 不 import Docker SDK、控制面 service 或 store。
- `pkg/protocol` 只承载稳定 wire model，不依赖 `internal/**`。
- 普通请求/响应优先使用统一协议模型；SSE 等流式传输可保留手写 transport。
- 若以后引入代码生成，生成代码与手写 adapter 必须分目录；不能只改生成输出。

## 变更路由

### API 或协议

先修改或确认 `api/` 契约，再检查：

1. `pkg/protocol/` 的字段、单位和枚举；
2. `internal/api/` 的请求映射、响应映射和错误语义；
3. `internal/runner/` 或 `internal/runnerclient/` 的 SSE 行为；
4. `sdk/go/` 的公共方法和语言原生类型；
5. contract tests、README 和相关设计文档。

不得在未确认迁移方案时删除、重命名或改变公共字段、operation ID、状态值和 SSE event 类型。

### 生命周期或 Docker runtime

- 生命周期 handler 只提交意图，不直接串行完成全部 Docker 操作。
- 每个 runtime 操作都要考虑请求重试、`sandboxd` 崩溃和重启恢复。
- labels 和本地目录均视为恢复协议的一部分；新增或改变格式时要考虑旧资源。
- 删除成功的定义是容器、workspace 和 runner socket 等受管资源都已清理或已进入可重试的清理流程。
- Docker 集成测试必须使用 MiniSandbox labels 精确定位资源，并在成功和失败路径都清理。

### Runner 或进程管理

- `argv` 与 `shell` 请求互斥，默认优先 `argv`。
- 所有执行都必须有可追踪 execution ID、确定的退出事件和单调递增的事件序号。
- stdout/stderr 保持区分，背压和客户端断开不能无限占用内存。
- timeout、显式 cancel 和容器关闭共享同一套进程组终止语义。
- `sandbox-init` 负责 PID 1 和孤儿回收；不要让通用 `wait4(-1)` 与 runner 的 `exec.Cmd.Wait` 竞争。
- runner 创建用户进程时必须过滤自身认证和配置环境变量。

### 配置、SDK 和文档

- 配置默认值、示例配置、校验和文档必须保持一致。
- SDK 方法应封装稳定 API，不在多个位置重复拼装私有 HTTP 路径。
- public SDK 使用 `time.Duration` 等 Go 原生类型；wire 单位必须在协议字段中明确。
- 用户可见或运维可见的行为变化应同步更新 `docs/` 和根 README 的相关入口。
- 长篇说明放在 `docs/`；包级 README 只保留构建、使用入口和必要约束。
- README 到仓库文档使用相对链接，使链接与当前分支或 tag 一致。

## 验证命令

常规 Go 变更至少运行：

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
```

优先运行受影响包的聚焦测试：

```bash
go test ./internal/domain/...
go test ./internal/api/...
go test ./internal/runner/...
```

涉及容器内二进制、信号或进程行为时，还要验证 Linux 构建：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/runnerd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/sandbox-init
```

涉及 Docker adapter、恢复或清理语义时，运行 opt-in integration/security tests。若本机没有 Docker、Linux 或必要权限，不得以普通单测代称这些检查；在最终交付中明确说明未运行的项目和原因。

## 必须先确认的变更

实施以下变更前先向用户确认：

- 破坏性的 API、SDK、配置、协议、状态或 label 变更；
- 新增生产依赖、外部服务或宿主机守护进程；
- 新增 privileged、capability、Docker socket 暴露、宿主机路径挂载或网络监听；
- 改变 sandbox 隔离、租户鉴权、配额、TTL、取消或清理语义；
- 大规模跨包重组或替换 runtime/store 方案；
- 有数据迁移或旧 sandbox 恢复兼容风险的持久化变更。

## 禁止事项

- 不得把业务逻辑堆入 HTTP handler 或 `main.go`。
- 不得绕过期望状态和 reconciler 直接制造不可恢复的生命周期状态。
- 不得用临时宿主机 shell 执行替代 runner 协议。
- 不得把 Docker socket、宿主机凭据或其他 sandbox 的控制能力交给 runner。
- 不得把秘密写入 labels、日志、错误响应、测试快照或 Git。
- 不得以直接编辑生成文件或嵌入二进制作为唯一修复。
- 不得让 API 契约、实现、SDK 和文档发生无说明漂移。
- 不得混入与当前任务无关的组件修改。
- 不得修改或提交 `OpenSandbox/` 参考仓库内容。

## Review 重点

代码审查按以下优先级检查：

1. 是否扩大宿主机权限、容器逃逸面或跨 sandbox 影响范围；
2. 是否破坏 API、SSE、状态、配置或 labels 的兼容性；
3. reconcile、重试、崩溃恢复和删除是否仍然幂等；
4. timeout/cancel 是否会遗留子进程、容器、目录或 socket；
5. 协议、实现、SDK、文档和测试是否同步；
6. 是否缺少针对行为变化的回归测试；
7. 是否存在不必要的抽象、配置或超出第一版范围的功能。
