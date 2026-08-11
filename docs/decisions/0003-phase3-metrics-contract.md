# ADR-0003：固定 Phase 3 Metrics 依赖与契约

- 状态：已接受（2026-08-11，依据 G7 与 P3-006）
- 决策日期：2026-08-11
- 对应任务：P3-006
- 适用范围：Phase 3 控制面 Prometheus 指标

## 1. 背景

Phase 3 需要暴露生命周期、收敛、恢复和 runner 健康信号，但不能让 scrape
占用当前单连接 SQLite，也不能用高基数 label 泄露 sandbox、execution、镜像、
请求或错误细节。execution 数据目前没有跨 runner 重启的持久化事件总账，因此
控制面只能准确描述自己收到的请求和前台代理实际观察到的终态。

本文只固定依赖和 metric contract。P3-082 才允许引入依赖和实现 `/metrics`；
本任务不修改 `go.mod`、不注册路由，也不增加采样 goroutine。

## 2. 依赖决策

首个消费者必须精确引入：

```text
github.com/prometheus/client_golang@v1.24.1
```

选择规则是：使用 P3-006 决策日可用、非预发布且未撤回的最新正式版本；后续不
使用 `latest` 自动漂移。该版本采用 Apache-2.0，声明 Go 1.25，满足仓库的
Go 1.26 基线。项目由 Prometheus 组织维护，仍有正式 release 和安全修复渠道。

该版本生产依赖图包含 `beorn7/perks`、`cespare/xxhash/v2`、
`json-iterator/go`、`klauspost/compress`、`prometheus/client_model`、
`prometheus/common`、`prometheus/procfs`、`golang.org/x/sys` 和
`google.golang.org/protobuf` 等；`go-cmp`、`godebug` 与 `goleak` 只服务于
上游测试。P3-082 必须以实际 `go mod graph`、`go mod verify` 和许可证扫描
复核最终 module graph，不得单独强制传递依赖版本。

升级必须独立提交，审查 release notes、Go 版本、许可证、exposition 兼容性和
传递依赖，并运行 metric contract、并发 gather、全仓库测试与 vet。

## 3. Registry、并发与采集边界

- 每个服务实例构造并注入独立的 `prometheus.NewRegistry()`；禁止使用
  `prometheus.DefaultRegisterer`、`prometheus.DefaultGatherer`、`MustRegister`
  的全局用法或 `promhttp.Handler()`。
- `/metrics` 使用注入 registry 的 gatherer；不默认注册 Go collector 或
  process collector。以后若需要，必须显式配置并另行审查。
- collector 在组装期注册一次，业务路径只更新已注册对象。库导出的并发安全
  保证适用于并发更新与 gather；自定义 snapshot collector 还必须独立通过
  race test。
- 所有名称使用 `minisandbox_` 前缀。label value 只能来自本文固定枚举；禁止
  sandbox ID、execution ID、request ID、image、URL、error message、path、
  token、idempotency key 和用户 metadata。
- Store-backed gauge 由后台采样器使用短 timeout 读取 Store，构造完整不可变
  snapshot 后一次原子发布。scrape 只读取最后成功 snapshot，不查询 SQLite；
  失败保留旧 snapshot，并由 snapshot age 表达陈旧程度。
- counter 只在事实已经发生的唯一完成点递增；重试、重放和 CAS 冲突不得重复
  计算同一事实。histogram 使用固定 buckets，禁止运行时配置。

## 4. 固定 Metric Contract

直方图 bucket 的单位均为秒：

```text
request_buckets   = [0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1,
                     0.25, 0.5, 1, 2.5, 5]
operation_buckets = [0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5,
                     5, 10, 30, 60, 120]
```

Prometheus 自动附加的 `+Inf` bucket、`_sum` 和 `_count` 属于标准 exposition。

| Metric | Type / unit | 固定 labels | 唯一更新位置 |
|---|---|---|---|
| `minisandbox_sandbox_create_requests_total` | Counter / requests | `result`: `accepted`, `rejected`, `error` | create application 调用结束时递增一次；Store 已接受 intent 为 `accepted`，确定性客户端拒绝为 `rejected`，其余失败为 `error`。 |
| `minisandbox_sandbox_create_duration_seconds` | Histogram / seconds；`request_buckets` | 无 | 与 create request counter 同一结束点观察一次；包含控制面校验、admission 和 Store 提交，不包含异步 reconcile。 |
| `minisandbox_sandbox_state_count` | Gauge / sandboxes | `state`: `Creating`, `Running`, `Terminating`, `Terminated`, `Failed` | Store 周期采样成功后随完整 snapshot 原子替换；每个允许状态均输出，缺失值为零。 |
| `minisandbox_reconcile_total` | Counter / attempts | `operation`: `create`, `delete`, `expire`, `recover`, `health`, `cleanup`; `result`: `converged`, `retry_scheduled`, `blocked`, `error` | keyed lock 内一次实际 reconcile attempt 结束时递增；未取得 worker/operation slot、shutdown 和 CAS conflict 不计 attempt。 |
| `minisandbox_reconcile_duration_seconds` | Histogram / seconds；`operation_buckets` | `operation`: 同上 | 与 reconcile counter 同一 attempt 的结束点观察一次。 |
| `minisandbox_retry_scheduled_total` | Counter / retries | `operation`: 同上；`error_code`: `runtime_unavailable`, `runner_unhealthy`, `cleanup_pending`, `internal_error` | 新的 `next_reconcile_at` 与 attempt 通过 CAS 成功持久化后递增；CAS conflict 不递增。 |
| `minisandbox_cleanup_pending` | Gauge / sandboxes | 无 | Store 周期采样成功后随 snapshot 原子替换为 cleanup pending 记录数。 |
| `minisandbox_lease_expired_total` | Counter / leases | 无 | expiry CAS 首次成功把 desired intent 改为 Terminated 后递增；stale timer、no-op 和 CAS conflict 不递增。 |
| `minisandbox_orphan_observations_total` | Counter / observations | `classification`: `incomplete_bundle`, `unknown_schema`, `identity_mismatch`, `spec_hash_mismatch`, `security_profile_mismatch`, `network_namespace_mismatch`, `lease_untrusted`, `duplicate_resource` | 一次 inventory observation 成功写入或更新对应 anomaly 后递增；不是 active anomaly gauge。 |
| `minisandbox_runtime_docker_operations_total` | Counter / operations | `operation`: `ping`, `inventory`, `ensure_network`, `pull_image`, `ensure_sandbox`, `replace_compute`, `delete_sandbox`; `result`: `success`, `retryable_error`, `terminal_error` | Docker adapter 的一次真实 Engine operation 返回时递增；上层重试各自计数。 |
| `minisandbox_execution_requests_total` | Counter / requests | `mode`: `foreground`, `background`; `result`: `accepted`, `rejected`, `error` | 控制面 execution application 请求结束时递增一次；只表示本进程收到的请求。 |
| `minisandbox_execution_foreground_terminal_observed_total` | Counter / observations | `result`: `exited`, `failed`, `cancelled`, `timed_out` | 前台 SSE 代理首次接收并验证 terminal event 时递增；客户端在终态前断开不递增。 |
| `minisandbox_runner_probe_total` | Counter / probes | `result`: `healthy`, `unhealthy`, `error` | 一次实际 runner health probe 完成分类后递增；未发起的 probe 不计数。 |
| `minisandbox_metrics_snapshot_age_seconds` | Gauge / seconds | 无 | 每次 gather 以 `max(0, now-last_successful_snapshot_at)` 计算；服务尚无成功 snapshot 时不输出该 sample。 |

`operation` 表示语义操作而非任意函数名；扩充任一 label 枚举、修改 bucket、类型、
单位或更新位置都属于 metric contract 变更，必须先更新本 ADR 和对应测试。

## 5. Execution 指标的非目标

`execution_requests_total` 不是已启动 execution 数，也不保证跨 sandboxd 重启
连续；`execution_foreground_terminal_observed_total` 只覆盖当前控制面前台代理
看见的终态，不覆盖后台 execution、客户端提前断开后的终态或 runner 重启前
事件。Phase 3 禁止将二者相减推导当前运行数、成功率或后台权威总量。

若未来需要这类语义，必须先设计持久化 runner event/ledger、去重键、retention
和重启恢复，再增加名称明确的新 metric，不能改变本文已有 counter 的含义。

## 6. 测试与验收

P3-082 及后续 metric 任务必须至少验证：

1. 使用独立 registry，重复测试或多服务实例互不污染；
2. descriptor 的完整名称、type、help、label name 与允许值；
3. 两组 histogram buckets 精确相等，并包含标准 `+Inf`；
4. 非法 label value 在业务封装边界被拒绝，且没有动态 label name；
5. 并发 update/gather 通过 `go test -race`；
6. Store 采样超时或失败时 scrape 不访问 Store、不阻塞并保留旧 snapshot；
7. 未显式注册 Go/process collector 时不出现对应 metric；
8. execution 请求、前台 terminal observation、后台任务和断开场景不混计。

## 7. 官方依据

- [client_golang v1.24.1 release](https://github.com/prometheus/client_golang/releases/tag/v1.24.1)
- [client_golang v1.24.1 go.mod](https://github.com/prometheus/client_golang/blob/v1.24.1/go.mod)
- [client_golang Apache-2.0 license](https://github.com/prometheus/client_golang/blob/v1.24.1/LICENSE)
- [prometheus package 文档](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus)
- [promhttp package 文档](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp)
