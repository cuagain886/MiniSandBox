# ADR-0001：固定 Phase 1 生产依赖

- 状态：提议，等待仓库所有者确认
- 决策日期：2026-07-25
- 对应任务：P1-000
- 适用范围：Phase 1 生命周期控制面

## 1. 背景

Phase 1 需要三类标准库不能直接提供的能力：

1. 通过 Docker Engine API 管理 sandbox 容器和 volume；
2. 在 `CGO_ENABLED=0` 条件下持久化 sandbox 的期望状态；
3. 从 YAML 文件加载 `sandboxd` 配置，并拒绝未知字段。

本阶段保持轻量，不引入 Web framework、ORM、migration framework、依赖注入
framework 或日志 framework。依赖必须适合 Go 1.26、`linux/amd64` 静态构建，
并采用允许商业使用和再分发的许可证。

本文只固定依赖决策。仓库所有者确认本文前，不得修改 `go.mod` 或 `go.sum`，
也不得在生产代码中导入这些模块。

## 2. 决策摘要

版本信息以 2026-07-25 的正式发布版本为准：

| 能力 | 选择 | Phase 1 固定版本 | 许可证 | 最低 Go 版本 |
|---|---|---:|---|---:|
| Docker Engine client | `github.com/moby/moby/client` | `v0.5.0` | Apache-2.0 | Go 1.24 |
| Docker Engine API types | `github.com/moby/moby/api` | `v1.55.0` | Apache-2.0 | Go 1.24 |
| SQLite driver | `modernc.org/sqlite` | `v1.54.0` | BSD-3-Clause | Go 1.25 |
| YAML v3 parser | `go.yaml.in/yaml/v3` | `v3.0.4` | Apache-2.0、MIT | Go 1.16 |

`client` 和 `api` 共同构成 Docker SDK 决策，不把它们视为两类独立能力。
四个模块声明的最低 Go 版本都不高于本仓库的 Go 1.26。

后续引入依赖时必须使用表中的完整 module path 和精确版本，不能使用
`latest`、版本范围、伪版本或预发布版本。

## 3. Docker SDK

### 3.1 选择

选择：

```text
github.com/moby/moby/client@v0.5.0
github.com/moby/moby/api@v1.55.0
```

Moby 从 Docker Engine 29 开始弃用不会继续更新的
`github.com/docker/docker` 根模块，并把受支持的 Engine client 与 API types
拆成独立模块。`client v0.5.0` 自身固定依赖 `api v1.55.0`，因此 Phase 1
把两者作为同一组升级和验证，避免 client 与请求类型错配。

MiniSandbox 直接使用低层 Engine API，因为 Docker adapter 需要清楚控制
inspect、pull、create、copy、start、stop 和 remove 的顺序、超时与幂等语义。
不选择更高层的 `github.com/docker/go-sdk`，以免其便利封装隐藏资源创建和
清理步骤，也避免引入本阶段不需要的抽象。

### 3.2 兼容性与依赖成本

- 两个模块都是 Moby 项目公开支持的 Go 模块，并使用 Apache-2.0。
- `client` 会带入 `api`、OCI image spec、连接辅助库和 OpenTelemetry HTTP
  instrumentation 等依赖，依赖图明显大于其他两个选择。
- 这部分成本可以接受，因为自行维护 Engine HTTP model、版本协商和错误解析
  的兼容风险更高。
- adapter 只在 `internal/runtime/docker` 中导入 SDK；domain、application、
  runner 和公共 protocol 不得依赖 SDK 类型。
- P1-036 必须启用 API version negotiation，并用窄接口包住实际使用的方法，
  使普通单元测试不需要 Docker daemon。

### 3.3 不选择的方案

| 方案 | 不选择原因 |
|---|---|
| `github.com/docker/docker` | 从 Docker Engine 29 起已弃用且不再更新，不适合作为新项目依赖。 |
| `github.com/docker/go-sdk` | 更高层且覆盖更多 Docker 使用场景；Phase 1 需要可审计的低层生命周期步骤。 |
| 手写 Engine HTTP client | 会重复维护 API types、版本协商、错误模型和跨 Engine 版本兼容。 |
| 调用 `docker` CLI | 依赖宿主机二进制和 shell，不利于超时、错误分类、恢复及测试。 |

## 4. SQLite driver

### 4.1 选择

选择：

```text
modernc.org/sqlite@v1.54.0
```

该模块是面向 `database/sql` 的 CGo-free SQLite driver，符合
`CGO_ENABLED=0` 的静态构建要求。它不要求宿主机安装 C compiler 或 SQLite
动态库，适合构建和分发单一 `sandboxd` 二进制。

Phase 1 直接使用 `database/sql` 和手写、版本化 migration，不在 driver
之上增加 ORM 或 migration framework。

### 4.2 兼容性与依赖成本

- 模块使用 BSD-3-Clause，并声明最低 Go 1.25。
- 主要依赖包括 `modernc.org/libc`、`modernc.org/fileutil`、
  `modernc.org/mathutil` 和 `golang.org/x/sys`；它比 CGo driver 的 Go
  依赖图更大，也会增加构建时间和二进制体积。
- 官方文档特别要求使用该版本 `go.mod` 中匹配的 `modernc.org/libc`
  版本。MiniSandbox 不单独强制升级或降级 `modernc.org/libc`，由
  `modernc.org/sqlite` 的 module graph 决定。
- P1-014 必须在 `CGO_ENABLED=0` 下构建 `sandboxd`，并验证 open、ping、
  WAL、foreign keys、busy timeout 和 close。
- 后续 migration 和 CAS 测试必须使用真实临时 SQLite 数据库，不能只使用
  SQL mock。

### 4.3 不选择的方案

| 方案 | 不选择原因 |
|---|---|
| `github.com/mattn/go-sqlite3` | 依赖 CGo，不满足 Phase 1 的静态构建约束。 |
| 外部 SQLite 服务或其他数据库 | 扩大部署和故障边界，偏离单机 Docker-first 的第一版。 |
| 自行实现持久化文件格式 | 需要额外解决事务、并发、崩溃恢复和 schema 演进。 |

## 5. YAML v3 parser

### 5.1 选择

选择：

```text
go.yaml.in/yaml/v3@v3.0.4
```

该模块由官方 YAML 组织维护，是原 `go-yaml/yaml` 项目的维护分支。v3 API
已冻结，只接收安全修复；这符合 Phase 1 要求的 YAML v3 行为稳定性，但不应
被理解为仍会获得常规功能更新。

P1-010 必须使用 `yaml.NewDecoder` 和 `KnownFields(true)` 拒绝未知字段。
配置加载只做 YAML 到配置模型的映射，不支持模板、任意类型反序列化或隐式
环境变量展开。

### 5.2 兼容性与依赖成本

- 模块使用 Apache-2.0 和 MIT 双许可证，并声明最低 Go 1.16。
- `go.mod` 只声明测试支持模块 `gopkg.in/check.v1`，生产解析路径主要依赖
  Go 标准库，依赖成本最低。
- YAML v3 保留部分 YAML 1.1 兼容行为，因此安全相关配置必须解码到明确的
  Go 字段类型，不能先解码到 `map[string]any` 再猜测类型。
- Phase 1 只接受 v3 的安全修复版本。迁移到官方推荐的 v4 属于解析行为变更，
  必须另建 ADR，并重新执行完整配置 fixture 和错误语义测试。

### 5.3 不选择的方案

| 方案 | 不选择原因 |
|---|---|
| `gopkg.in/yaml.v3` | 原上游已声明不再维护；新代码应使用 YAML 组织接管后的模块路径。 |
| `go.yaml.in/yaml/v4` | 官方推荐新项目使用，但超出 Phase 1 已确定的 YAML v3 约束，且升级需要单独审查解析兼容性。 |
| JSON-only 配置 | 与 Phase 1 的示例配置和可运维性目标不一致。 |
| 自写 YAML parser | 格式复杂且安全边界难以审计，不属于 sandbox runtime 的核心能力。 |

## 6. 固定与引入策略

确认本文后，依赖只在第一个真实消费者任务中引入：

| 任务 | 允许引入的模块 |
|---|---|
| P1-010 | `go.yaml.in/yaml/v3@v3.0.4` |
| P1-014 | `modernc.org/sqlite@v1.54.0` |
| P1-036 | `github.com/moby/moby/client@v0.5.0` 和 `github.com/moby/moby/api@v1.55.0` |

每次引入必须满足：

1. 使用精确版本执行 `go get`，随后执行 `go mod tidy`；
2. 提交 `go.mod` 和 `go.sum`，不使用 `replace` 绕过模块路径或版本；
3. 检查 `go mod graph`，说明新增的直接依赖和重要传递依赖；
4. 执行 `go mod verify`、受影响包测试、`go test ./...` 和 `go vet ./...`；
5. SQLite 引入任务额外执行 `CGO_ENABLED=0` 的 Linux 构建；
6. Docker 引入任务额外验证 client/API 版本组合和 API version negotiation；
7. 若安全扫描或许可证检查发现不可接受问题，停止引入并修订本 ADR。

`go.sum` 是依赖完整性记录，必须随首次引入或升级提交。不得 vendor 这些模块，
除非以后有离线构建需求并通过单独决策。

## 7. 升级策略

依赖不自动漂移。安全公告、Phase 结束或季度依赖审查可以触发升级，但每次
升级都必须使用独立提交并通过对应测试：

- Docker：`client` 与 `api` 成组审查；先阅读两者 release notes，再运行
  fake Engine 单测和真实 Linux Docker integration tests。不能仅按 Docker
  Engine 产品版本号推导 Go 模块版本。
- SQLite：审查 bundled SQLite 版本、retracted versions 和
  `modernc.org/libc` 约束；运行 migration、CAS、WAL、重启恢复和
  `CGO_ENABLED=0` 构建测试。
- YAML v3：只接受明确需要的安全修复 patch；运行全部配置 fixture、未知字段
  和错误位置测试。升级到 v4 必须新建 ADR。

任何升级都禁止使用 `@latest` 直接写入主分支。若升级改变公共配置、持久化
格式、Docker 资源恢复或错误语义，还必须同步相应设计文档和兼容性测试。

## 8. 风险与后果

正面后果：

- Docker 使用官方低层 API，避免宿主机 shell 和 CLI 依赖；
- SQLite 保持单二进制部署，不引入 C toolchain；
- YAML parser 支持严格字段检查，且不会依赖已停止维护的原模块路径；
- 依赖只停留在 adapter/config 边界，不污染 domain 和 protocol。

需要接受的代价：

- Docker SDK 和 `modernc.org/sqlite` 会显著增加 module graph 与构建体积；
- Moby 拆分模块目前仍是 `v0.x` client，升级时必须认真审查兼容性；
- YAML v3 只有安全维护，未来迁移 v4 需要单独投入；
- SQLite 的纯 Go 移植需要更严格的真实数据库与跨平台构建验证。

## 9. 官方依据

- [Moby Go modules 与旧模块弃用说明](https://github.com/moby/moby#go-modules)
- [Moby client v0.5.0 release](https://github.com/moby/moby/releases/tag/client%2Fv0.5.0)
- [Moby client v0.5.0 go.mod](https://github.com/moby/moby/blob/client/v0.5.0/client/go.mod)
- [Moby API v1.55.0 go.mod](https://github.com/moby/moby/blob/api/v1.55.0/api/go.mod)
- [modernc.org/sqlite v1.54.0 文档](https://pkg.go.dev/modernc.org/sqlite@v1.54.0)
- [modernc.org/sqlite v1.54.0 go.mod](https://gitlab.com/cznic/sqlite/-/blob/v1.54.0/go.mod)
- [YAML 组织维护状态和版本策略](https://github.com/yaml/go-yaml#project-status)
- [go.yaml.in/yaml/v3 v3.0.4 文档](https://pkg.go.dev/go.yaml.in/yaml/v3@v3.0.4)
