# Phase 4：Agent 体验开发计划

> - 状态：已完成，`P4-000～P4-045` 验收通过
> - 前置阶段：[Phase 3：可靠性开发计划](./phase-3-reliability-development-plan.md)
> - SDK 基线：[Go SDK 易用化开发计划](./phase-sdk-go-productization-development-plan.md)
> - 上位设计：[全 Go Agent Sandbox Runtime 设计](./all-go-agent-sandbox-runtime-design.md)

## 1. 目标

Phase 1～Phase 3 已经完成 sandbox 生命周期、命令执行、租约、恢复和基础 Go SDK。Phase 4 不再重复这些能力，只补齐 Agent 实际使用 sandbox 时缺少的功能。

完成后，调用方应当能够：

```text
创建并等待 sandbox
  → 查询可用能力
  → 上传和管理 workspace 文件
  → 执行命令并下载结果
  → 打开交互式 PTY
  → 访问 sandbox 内启动的 HTTP 服务
  → 使用 Go、TypeScript 或 Python SDK 完成上述操作
  → 按配置预拉取常用镜像
  → 删除 sandbox
```

## 2. 开发原则

本阶段采用以下简化原则：

1. 只实现当前阶段明确需要的功能，不提前建设通用插件、发布平台或扩展框架；
2. 继续沿用 `sandboxd → application → runnerclient → runnerd` 的现有结构；
3. 公共协议一次确定，runner、控制面和 SDK 按同一协议实现；
4. 不为尚未出现的使用场景增加复杂配置、状态、重试、恢复或观测机制；
5. 不为每个 syscall、消息帧或 handler 单独拆任务；
6. 普通功能任务不单独新增一套测试，集中在阶段性验收任务中验证；
7. 不建立测试矩阵、API 快照、跨语言 fixture 平台或发布流水线；
8. 每个任务完成后仍按仓库规范独立提交，但文档不记录提交标识；
9. Phase 4 不修改已经稳定的 lifecycle、execution、TTL、renew 和清理语义。

## 3. 实现范围

### 3.1 本阶段实现

- sandbox capabilities 查询；
- workspace 文件 stat、list、mkdir、upload、download、move 和 delete；
- 基于 WebSocket 的交互式 PTY；
- 访问当前 sandbox loopback 服务的 HTTP port proxy；
- Go SDK 对上述功能的封装；
- 基础 TypeScript SDK；
- 基础 Python SDK；
- 配置驱动的镜像预拉取；
- 用户文档、示例和真实 Docker 验收。

### 3.2 必要功能约束

这些约束直接决定功能含义，不扩展为额外安全工程：

- 文件 API 的所有路径都相对于 `/workspace`，不能访问 workspace 之外的路径；
- 文件内容使用流式上传和下载，不经过命令执行接口中转；
- PTY 关闭、超时或 sandbox 删除时终止对应进程组；
- port proxy 只访问当前 sandbox 的 loopback HTTP 服务；
- runner 仍不接触 Docker Socket，也不能管理其他 sandbox；
- sandboxd 不在宿主机执行用户命令，也不发布 Docker host port。

### 3.3 本阶段不做

- workspace 之外的文件管理；
- archive 解包、文件同步、watch、搜索和内容 patch；
- PTY 重连、多人共享和会话回放；
- raw TCP、UDP、CONNECT、公网分享地址和浏览器控制台；
- npm、PyPI 或公共 Go module 发布；
- 镜像缓存淘汰、镜像 GC 和 registry credential 管理；
- Pool、快照、Kubernetes 或新的隔离 runtime。

## 4. 功能设计

### 4.1 Capabilities

新增公共接口：

```text
GET /v1/sandboxes/{sandbox_id}/capabilities
```

runner 内部提供对应的固定接口：

```text
GET /v1/capabilities
```

返回当前 runner 实际提供的功能：

```json
{
  "files": true,
  "pty": true,
  "http_port_proxy": true
}
```

Go SDK 的 `WaitReady` 先等待 sandbox 进入 `Running`，再读取 capabilities。TypeScript 和 Python SDK 使用相同语义。

### 4.2 Files

公共文件接口：

```text
POST   /v1/sandboxes/{id}/files/stat
POST   /v1/sandboxes/{id}/directories/list
POST   /v1/sandboxes/{id}/directories
PUT    /v1/sandboxes/{id}/files/content
GET    /v1/sandboxes/{id}/files/content
POST   /v1/sandboxes/{id}/files/move
POST   /v1/sandboxes/{id}/files/delete
```

runner 内部使用对应的 `/v1/files/**` 和 `/v1/directories/**` 固定接口，不接收 sandbox ID。

路径规则保持简单：

- `.` 表示 workspace 根目录；
- `src/main.go` 表示 `/workspace/src/main.go`；
- 拒绝绝对路径和包含 `..` 的路径；
- Linux 文件操作以 workspace 根目录为起点解析，最终结果不能离开 workspace。

Stat 返回：

```text
path
type
size_bytes
mode
modified_at
```

List 返回指定目录的直接子项。第一版不实现分页快照或目录变化跟踪。

Upload 使用 `application/octet-stream`：

```text
PUT /v1/sandboxes/{id}/files/content?path=src/main.go&overwrite=false&create_parents=true
```

Download 直接返回文件字节：

```text
GET /v1/sandboxes/{id}/files/content?path=bin/app
```

Mkdir、Move 和 Delete 使用简单 JSON 请求。Delete 支持删除文件、空目录以及调用方明确指定的递归目录。

### 4.3 PTY

外部和内部均使用 WebSocket：

```text
GET /v1/sandboxes/{id}/pty
GET /v1/pty
Sec-WebSocket-Protocol: minisandbox.pty.v1
```

连接建立后的第一条文本消息启动 PTY：

```json
{
  "type": "start",
  "argv": ["/bin/bash"],
  "cwd": ".",
  "env": {},
  "cols": 120,
  "rows": 40,
  "timeout_seconds": 3600
}
```

后续消息：

- client binary：stdin；
- client text：窗口 resize；
- server binary：PTY 合并输出；
- server text：started、terminal 或 error。

一条 WebSocket 对应一个 PTY session。连接结束时关闭 PTY，并沿用 runner 已有的进程组终止方式。

### 4.4 HTTP Port Proxy

公共接口：

```text
ANY /v1/sandboxes/{id}/ports/{port}/http/{path...}
```

runner 内部接口：

```text
ANY /v1/ports/{port}/http/{path...}
```

请求链路：

```text
SDK 或 HTTP client
  → sandboxd 确认 sandbox Running
  → runnerclient 通过当前 sandbox 的 Unix Socket 转发
  → runner 连接 127.0.0.1:port
  → 返回 sandbox 内 HTTP 服务响应
```

第一版只支持普通 HTTP 请求和响应，不支持 WebSocket、CONNECT、raw TCP 或 UDP。控制面认证信息不转发给 sandbox 内应用。

该设计不改变 Phase 2 的网络模型：无论 outbound 是否开启，runner 都在当前 sandbox 网络命名空间中访问 loopback，不创建 Docker published port。

### 4.5 SDK

Go SDK 延续现有资源对象：

```go
sandbox, err := client.Create(ctx, request)
info, err := sandbox.WaitReady(ctx)

entry, err := sandbox.Files().Stat(ctx, "src/main.go")
err = sandbox.Files().Upload(ctx, "src/main.go", source)
reader, err := sandbox.Files().Download(ctx, "bin/app")

pty, err := sandbox.OpenPTY(ctx, request)
response, err := sandbox.PortHTTP(ctx, 8080, request)
```

TypeScript SDK 使用 Promise、AsyncIterable、Readable 和 Uint8Array。Python SDK 使用普通同步 client、iterator 和 binary file-like object。第一版只需要覆盖本项目已经存在的 lifecycle/execution，再增加 capabilities、files、PTY 和 port proxy。

三种 SDK 都只访问 sandboxd 的公共接口，不直接接触 runner 地址、token 或 Unix Socket。

### 4.6 Image Pre-pull

配置示例：

```yaml
runtime:
  prepull_images:
    - image: "golang:1.26"
      platform: "linux/amd64"
```

`sandboxd` 启动后检查配置中的镜像；本地不存在时调用现有 Docker runtime 拉取。预拉取在后台执行，不增加新的公共管理 API。

## 5. 配置

Phase 4 只增加功能直接需要的配置：

```yaml
files:
  enabled: true
  max_upload_bytes: 33554432
  max_download_bytes: 67108864

pty:
  enabled: true
  max_concurrent_sessions: 2
  default_timeout: "1h"

port_proxy:
  enabled: true
  min_port: 1024
  max_port: 65535

runtime:
  prepull_images: []
```

配置模型、默认值和 `configs/sandboxd.example.yaml` 同步修改。普通请求不能临时开启服务端已关闭的功能。

## 6. 阶段划分与测试时点

普通功能任务不为每个小步骤单独设计测试。新增测试和完整回归只集中在以下阶段性时点：

| 验收任务 | 验收范围 |
|---|---|
| `P4-017` | Files 完整工作流 |
| `P4-029` | PTY 与 HTTP Port Proxy |
| `P4-042` | 三种 SDK 与 Image Pre-pull |
| `P4-045` | Phase 4 最终回归 |

阶段性验收只覆盖已经实现的真实功能，不为尚不存在的故障模式编写假设性用例。

## 7. 任务总览

| 阶段 | 任务 | 交付结果 |
|---|---:|---|
| A. 协议与配置 | P4-000～P4-006 | API、协议模型、依赖与配置 |
| B. Files | P4-007～P4-017 | workspace 文件管理完整链路 |
| C. PTY | P4-018～P4-024 | 交互终端完整链路 |
| D. HTTP Port Proxy | P4-025～P4-029 | sandbox loopback HTTP 访问 |
| E. SDK | P4-030～P4-039 | Go、TypeScript 和 Python SDK |
| F. Image Pre-pull | P4-040～P4-042 | 配置驱动镜像预拉取 |
| G. 收尾 | P4-043～P4-045 | 文档、真实工作流和最终验收 |

## 8. 详细任务

### A. 协议与配置

### P4-000：确认 Phase 4 开发基线

- **目标**：确认 Phase 3 和 SDK Phase 已完成，现有服务能够创建 sandbox 并执行命令。
- **实现**：读取现有验收报告和当前项目状态，记录 Phase 4 起点与已知环境限制。
- **依赖**：Phase 3、SDK Phase。

### P4-001：确定 Phase 4 依赖

- **目标**：确定 files、WebSocket、PTY、TypeScript 和 Python 实现直接需要的依赖。
- **实现**：选择最少可用依赖及支持版本，集中更新依赖文件，不建立额外依赖治理流程。
- **依赖**：P4-000。

### P4-002：定义 capabilities 协议

- **目标**：让调用方知道当前 sandbox 是否支持 files、PTY 和 port proxy。
- **实现**：更新 OpenAPI、`pkg/protocol` 和内部 runner 协议模型。
- **依赖**：P4-001。

### P4-003：定义 Files 协议

- **目标**：确定 stat、list、mkdir、upload、download、move 和 delete 的请求与响应。
- **实现**：更新公共和内部 OpenAPI，并增加对应协议模型。
- **依赖**：P4-002。

### P4-004：定义 PTY 协议

- **目标**：确定 PTY WebSocket endpoint 和 start、resize、terminal 消息。
- **实现**：在公共和内部 API 中增加 PTY 协议说明与消息模型。
- **依赖**：P4-003。

### P4-005：定义 HTTP Port Proxy 协议

- **目标**：确定通过指定 sandbox ID 和 port 访问 HTTP 服务的方式。
- **实现**：增加公共和内部固定路由，并说明 method、path、body 和 response 转发语义。
- **依赖**：P4-004。

### P4-006：增加 Phase 4 配置模型

- **目标**：让 files、PTY、port proxy 和 pre-pull 可以通过配置开启。
- **实现**：增加第 5 节配置字段、默认值和示例配置，并把 runner 需要的值传入 runner bootstrap。
- **依赖**：P4-001～P4-005。

### B. Files

### P4-007：建立 Files service

- **目标**：在 runner 中建立 workspace 文件服务入口。
- **实现**：打开 workspace 根目录并实现统一的相对路径解析。
- **依赖**：P4-003、P4-006。

### P4-008：实现 Stat 和 List

- **目标**：查询文件 metadata 和目录直接子项。
- **实现**：在 Files service 中增加 stat 与 list 方法并返回协议模型。
- **依赖**：P4-007。

### P4-009：实现 Mkdir

- **目标**：创建单层或多层 workspace 目录。
- **实现**：增加 mkdir 方法和 `parents` 选项。
- **依赖**：P4-007。

### P4-010：实现 Upload

- **目标**：把二进制内容上传到 workspace 文件。
- **实现**：支持创建父目录、是否覆盖和上传大小限制。
- **依赖**：P4-007。

### P4-011：实现 Download

- **目标**：流式读取 workspace 中的普通文件。
- **实现**：返回 reader，并在完成或 context 结束时关闭文件。
- **依赖**：P4-007。

### P4-012：实现 Move 和 Delete

- **目标**：移动文件或目录，并删除文件、空目录或指定的递归目录。
- **实现**：增加 move、delete 方法及 overwrite、recursive 选项。
- **依赖**：P4-007～P4-011。

### P4-013：实现 runner Files handlers

- **目标**：通过 runner 固定 HTTP endpoint 使用 Files service。
- **实现**：实现 stat、list、mkdir、upload、download、move 和 delete handlers。
- **依赖**：P4-008～P4-012。

### P4-014：实现 RunnerClient Files 方法

- **目标**：让 sandboxd 通过当前 sandbox 的 Unix Socket 调用 Files API。
- **实现**：在 `internal/runnerclient` 增加对应 typed methods 和流式 body 转发。
- **依赖**：P4-013。

### P4-015：实现 Files application service

- **目标**：在 application 层组织 sandbox 状态检查和 RunnerClient 调用。
- **实现**：增加 Files 用例服务，不把业务逻辑放入 HTTP handler。
- **依赖**：P4-014。

### P4-016：实现公共 Files handlers

- **目标**：向 SDK 和普通 HTTP 调用方开放 Files 功能。
- **实现**：实现公共路由、请求映射、流式上传和下载代理，并接入 capabilities。
- **依赖**：P4-015。

### P4-017：阶段性验收 Files

- **目标**：集中验证 P4-007～P4-016。
- **验收内容**：在真实 sandbox 中完成 mkdir、upload、stat、list、download、move 和 delete；验证二进制内容一致，并运行受影响 Go 包回归。
- **依赖**：P4-007～P4-016。

### C. PTY

### P4-018：建立 PTY session

- **目标**：在 runner 中表示一条 PTY 连接和对应用户进程。
- **实现**：定义 session、start request、状态和关闭方法。
- **依赖**：P4-001、P4-004、P4-006。

### P4-019：启动 PTY 进程

- **目标**：为请求命令创建伪终端并启动进程。
- **实现**：设置 cwd、env、窗口大小和 controlling terminal，沿用 runner 非 root 身份。
- **依赖**：P4-018。

### P4-020：实现 PTY 输入、输出和 Resize

- **目标**：让调用方可以交互输入、读取终端输出并调整窗口。
- **实现**：连接 WebSocket 消息与 PTY stdin、output、resize。
- **依赖**：P4-019。

### P4-021：实现 PTY 结束处理

- **目标**：命令退出、连接关闭、timeout 或 sandbox 删除时结束 session。
- **实现**：复用 runner 的进程组终止和 wait 逻辑，返回 terminal 消息并关闭 PTY。
- **依赖**：P4-020。

### P4-022：实现 runner PTY WebSocket handler

- **目标**：通过 runner 内部 `/v1/pty` 使用 PTY session。
- **实现**：完成 WebSocket handshake、消息循环和 session 关闭。
- **依赖**：P4-018～P4-021。

### P4-023：实现 RunnerClient PTY bridge

- **目标**：让 sandboxd 通过 runner Unix Socket 建立 PTY WebSocket。
- **实现**：增加内部 WebSocket client 和双向消息 bridge。
- **依赖**：P4-022。

### P4-024：实现公共 PTY handler

- **目标**：向调用方开放 `/v1/sandboxes/{id}/pty`。
- **实现**：检查 sandbox 状态，建立 RunnerClient bridge，并在连接结束时释放 session。
- **依赖**：P4-023。

### D. HTTP Port Proxy

### P4-025：实现 runner loopback HTTP client

- **目标**：从 runner 访问当前 sandbox 的 loopback HTTP 服务。
- **实现**：根据 port、method、path 和 body 构造 HTTP 请求并返回响应。
- **依赖**：P4-005、P4-006。

### P4-026：实现 runner Port Proxy handler

- **目标**：通过 runner 固定路由转发 HTTP 请求。
- **实现**：校验 port，转发普通 HTTP header/body/status，并移除控制面认证信息。
- **依赖**：P4-025。

### P4-027：实现 RunnerClient 和 application Port 方法

- **目标**：让控制面调用当前 sandbox 的 port proxy。
- **实现**：增加 RunnerClient typed method 和 application service。
- **依赖**：P4-026。

### P4-028：实现公共 Port Proxy handler

- **目标**：向调用方开放 `/v1/sandboxes/{id}/ports/{port}/http/**`。
- **实现**：检查 sandbox 状态，通过 application service 转发请求和响应。
- **依赖**：P4-027。

### P4-029：阶段性验收 PTY 与 Port Proxy

- **目标**：集中验证 P4-018～P4-028。
- **验收内容**：在真实 sandbox 中打开 shell、发送输入、读取输出、resize 并正常退出；启动一个 HTTP 服务，通过 port proxy 完成 GET 和 POST；运行受影响 Go 包回归。
- **依赖**：P4-018～P4-028。

### E. SDK

### P4-030：扩展 Go SDK Capabilities

- **目标**：通过现有 `Sandbox` 资源对象查询能力并等待 ready。
- **实现**：增加 `Capabilities` 和 `WaitReady`。
- **依赖**：P4-002、P4-029、SDK Phase。

### P4-031：扩展 Go SDK Files

- **目标**：用 Go 原生 reader、writer 和结果类型管理 workspace 文件。
- **实现**：增加 Stat、List、Mkdir、Upload、Download、Move 和 Delete。
- **依赖**：P4-017、P4-030。

### P4-032：扩展 Go SDK PTY

- **目标**：通过 Go SDK 打开并使用交互式 PTY。
- **实现**：增加 PTY client、输入、输出、resize 和 close。
- **依赖**：P4-024、P4-030。

### P4-033：扩展 Go SDK PortHTTP

- **目标**：通过 Go SDK 调用 sandbox 内 HTTP 服务。
- **实现**：增加 `PortHTTP`，使用 Go 原生 `http.Request` 和 `http.Response` 风格。
- **依赖**：P4-028、P4-030。

### P4-034：建立 TypeScript SDK 基础

- **目标**：提供 TypeScript Client、Sandbox 和 Execution 基础对象。
- **实现**：封装当前 lifecycle、execution、错误和等待操作，面向 Node.js 使用。
- **依赖**：P4-001、SDK Phase 公共行为。

### P4-035：实现 TypeScript Agent 功能

- **目标**：在 TypeScript SDK 中支持 capabilities、files、PTY 和 port proxy。
- **实现**：使用 Promise、AsyncIterable、Readable 和 Uint8Array 提供对应方法。
- **依赖**：P4-030～P4-034。

### P4-036：建立 Python SDK 基础

- **目标**：提供 Python Client、Sandbox 和 Execution 基础对象。
- **实现**：封装当前 lifecycle、execution、错误和等待操作，先提供同步 API。
- **依赖**：P4-001、SDK Phase 公共行为。

### P4-037：实现 Python Agent 功能

- **目标**：在 Python SDK 中支持 capabilities、files、PTY 和 port proxy。
- **实现**：使用 iterator 和 binary file-like object 提供对应方法。
- **依赖**：P4-030～P4-036。

### P4-038：编写三种 SDK 示例

- **目标**：让用户可以直接看到同一 Agent workflow 在三种语言中的用法。
- **实现**：分别提供 create、wait、upload、run、download 和 delete 示例，并增加 PTY、port proxy 的简短示例。
- **依赖**：P4-031～P4-037。

### P4-039：整理 SDK 用户入口

- **目标**：让根 README 和各 SDK README 指向新的推荐用法。
- **实现**：更新安装方式、对象模型、功能列表和示例入口，不增加发布流程。
- **依赖**：P4-038。

### F. Image Pre-pull

### P4-040：实现 Pre-pull 配置读取

- **目标**：读取需要在启动后准备的 image 和 platform 列表。
- **实现**：校验基本字段，并把列表交给 runtime 启动流程。
- **依赖**：P4-006。

### P4-041：实现镜像预拉取

- **目标**：启动后准备配置中的常用镜像。
- **实现**：本地已有镜像时跳过，否则调用现有 Docker runtime pull；任务在后台执行。
- **依赖**：P4-040、现有 runtime image pull。

### P4-042：阶段性验收 SDK 与 Pre-pull

- **目标**：集中验证 P4-030～P4-041。
- **验收内容**：Go、TypeScript 和 Python 分别完成 create、wait、upload、run、download、delete；配置的镜像能够在启动后被准备；运行三个 SDK 的阶段回归。
- **依赖**：P4-030～P4-041。

### G. 收尾

### P4-043：更新用户和配置文档

- **目标**：完整说明 Phase 4 新增功能如何使用。
- **实现**：更新根 README、配置示例和用户指南，介绍 Files、PTY、Port Proxy、SDK 与 Pre-pull。
- **依赖**：P4-042。

### P4-044：整理完整 Agent workflow

- **目标**：提供一条从创建 sandbox 到删除 sandbox 的完整演示。
- **实现**：组合 capabilities、upload、execution、download、PTY 和 port proxy，作为最终验收入口。
- **依赖**：P4-043。

### P4-045：Phase 4 最终验收

- **目标**：确认 Phase 4 的全部功能可以交付使用。
- **验收内容**：运行 Go 全仓测试和 vet、TypeScript/Python SDK 测试、Linux 构建、真实 Docker Agent workflow；确认 Files、PTY、Port Proxy、三种 SDK 和 Pre-pull 均可使用，并生成简短验收报告。
- **依赖**：P4-000～P4-044。

## 9. 阶段完成标准

全部任务完成后应满足：

- capabilities 可以报告 files、PTY 和 port proxy 是否可用；
- 用户可以上传、查询、列出、下载、移动和删除 workspace 文件；
- 用户可以打开交互 PTY、发送输入、resize 并读取输出；
- 用户可以访问当前 sandbox 内的 HTTP 服务；
- Go、TypeScript 和 Python SDK 都能完成核心 Agent workflow；
- 配置中的常用镜像可以自动预拉取；
- 现有 lifecycle、execution、TTL、renew、recovery 和删除功能继续工作；
- README 和用户指南给出可直接运行的使用方式；
- Phase 4 最终真实 Docker 验收通过。
