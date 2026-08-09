# Phase 3 启动前验收清单（P3-000）

## 1. 结论

**P3-000 验收状态：PASS。可以开始 P3-001。**

截至 2026-08-10（Asia/Shanghai），Phase 1/2 的验收报告、当前生产代码、
冻结版本和真实 Linux Docker 闭环均已重新核对。最终一次原生 Docker 全量运行中，
39 个顶层 integration/security test 全部执行并通过，无 FAIL、无 SKIP；验收后没有
`CLEANUP_PENDING`、未知受管资源、残留 execution 或协议漂移。

本任务只生成本清单，没有修改 TTL、Store、reconciler 或其他生产代码。按照仓库的
小提交规则，本报告提交位于被验证的 HEAD 之后；报告不能在自身内容中记录自身 commit，
最终提交 SHA 应从 Git 历史读取。

## 2. 被验证的不可变基线

| 项目 | SHA / 结果 | 判断 |
|---|---|---|
| P3-000 验证前 HEAD | `128da8102d4668059c55fe29e6bef59a442d8fe4` | 本次验证的仓库基线 |
| Phase 1 最终测试代码 | `adc0c6fbb5d39ea27710df01a080fca4203577d9` | `docs/reports/phase1-acceptance.md` 为 PASS |
| Phase 1 报告提交 | `9ae183ba003e0221d9eae138ab494d7f817a55ea` | 报告紧随被测代码提交 |
| Phase 2 最终测试代码 | `440c73c0fac180d9cf6cb4feec221c75cc880387` | `docs/reports/phase2-acceptance.md` 为 13/13 PASS |
| Phase 2 报告最新提交 | `36073e14fa360bd34f9f6087ce16e8269b8f1c52` | 补充 build attestation 边界，未改生产代码 |
| Phase 2 代码祖先关系 | PASS | `440c73c...` 是当前 HEAD 的祖先 |
| Phase 2 后生产代码差异 | 0 | `git diff 440c73c... HEAD -- . ':!docs/**'` 无输出 |
| 本地实现差异 | 0 | `cmd/api/internal/pkg/sdk/configs/tests/go.mod/go.sum` 无未提交修改 |

Phase 1 报告当前 blob 为 `e69990962c1881a264b3592b5d60fb955c949033`，
Phase 2 报告当前 blob 为 `f7f724a3ec1f56c2866a80ba410f64870679aeda`。
两个报告均已实际读取，而不是只根据文件存在性作判断。

## 3. 冻结版本与协议漂移检查

| 边界 | 当前版本 | P3-000 判断 |
|---|---:|---|
| SQLite schema | `1` | 只有 migration 1；尚无 lease、idempotency 或 anomaly 表 |
| Docker managed label schema | `1` | reader 精确接受 v1；`expires-at` 必须为空 |
| runner bootstrap / health protocol | `1` | 控制面与 runner 精确匹配，未知版本 fail closed |
| egress bootstrap protocol | `1` | sidecar bootstrap 精确匹配 |
| nft immutable rule schema | `1` | 固定内部 CIDR deny set 的编译 schema |
| 公共 lifecycle / execution API | Phase 2 冻结版本 | OpenAPI、protocol、SDK 和实现没有 Phase 2 后代码差异 |

结论：Phase 3 必须从既有 v1 兼容边界增量演进。尤其 P3-010 的 SQLite v2 和
Phase 3 Docker label v2 不能通过原地改变 v1 语义实现；runner 与 egress 协议也不能
在未递增版本时接受不兼容字段。

## 4. 验收环境与固定镜像

| 项目 | 值 |
|---|---|
| Windows Go | `go1.26.4 windows/amd64` |
| Linux | Ubuntu 24.04.2 LTS，WSL2 kernel `6.18.33.2-microsoft-standard-WSL2` |
| Linux Go | `go1.26.0 linux/amd64` |
| Docker | 原生 Ubuntu Docker Engine `29.1.3`，API `1.52` |
| Docker socket | `unix:///run/minisandbox-native-docker.sock` |
| storage / cgroup / runtime | overlayfs / cgroup v2 / runc |
| sandbox 镜像 | `debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818` |
| egress 镜像 | `minisandbox-egress-native@sha256:2080476bdf728fd6fa4c622d1d37ad297147d6586e16842c8cc9b735ea36a7da` |
| coding-agent 验收镜像 | `minisandbox-agent-native@sha256:9877ad72ea70f1f19756c8e25911426e59ec9650fbca0a9871884c4cc9f136b0` |

coding-agent 验收镜像由固定的
`golang:1.26-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599`
派生，只增加 `HOME=/tmp`。这是因为 MiniSandbox 固定 execution UID/GID 为非 root
`65532:65532`，基础镜像隐式得到的 `/root` 不能作为 Go build cache。该临时 tag 在
验收完成后已删除；固定基础镜像仍保留在本地，可按同一配置重建。

验收使用原生 dockerd，测试进程与容器共享同一 WSL2 内核。netns inode、nft、
UID/GID、capability、Unix Socket 和进程组证据均来自真实 Linux 路径，没有使用
Docker Desktop 路径代理或 fake runtime。

integration 测试进程以 root 运行，仅用于在 WSL loopback 上安装并回收本地“公网地址”
fixture；sandbox 内用户命令仍固定以 `65532:65532`、零 capability 执行，root 没有进入
runner execution 身份或公共 API 语义。

## 5. 静态检查、构建和真实 Docker 结果

| 检查 | 结果 | 说明 |
|---|---|---|
| tracked Go files `gofmt -l` | PASS | 无输出 |
| `go test ./...` | PASS | Windows 普通测试全部通过 |
| `go vet ./...` | PASS | 无输出 |
| Linux integration vet | PASS | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet -tags=integration ./tests/integration` |
| Linux/amd64 `sandboxd` | PASS | `CGO_ENABLED=0`，静态 ELF |
| Linux/amd64 `runnerd` | PASS | `CGO_ENABLED=0`，静态 ELF |
| Linux/amd64 `sandbox-init` | PASS | `CGO_ENABLED=0`，静态 ELF |
| Linux/amd64 `egressd` | PASS | `CGO_ENABLED=0`，静态 ELF |
| timeout 稳定性重复验证 | PASS | `TestExecutionTimeoutTerminatesProcessTree` 连续 3 次通过 |
| 最终全量 integration/security | **PASS** | 39/39 顶层测试，无 SKIP，117.382 秒 |

最终全量命令的关键环境如下：

```bash
GOMODCACHE=/home/xf/go/pkg/mod \
GOCACHE=/tmp/minisandbox-p3-000-go-cache \
GOFLAGS=-buildvcs=false \
MINISANDBOX_INTEGRATION=1 \
MINISANDBOX_TEST_DOCKER_HOST=unix:///run/minisandbox-native-docker.sock \
MINISANDBOX_TEST_DATA_ROOT=/tmp/minisandbox-p3-000-data \
MINISANDBOX_TEST_IMAGE=debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
MINISANDBOX_TEST_EGRESS_IMAGE=minisandbox-egress-native@sha256:2080476bdf728fd6fa4c622d1d37ad297147d6586e16842c8cc9b735ea36a7da \
MINISANDBOX_TEST_AGENT_IMAGE=minisandbox-agent-native@sha256:9877ad72ea70f1f19756c8e25911426e59ec9650fbca0a9871884c4cc9f136b0 \
go test -v -tags=integration -count=1 -timeout=30m ./tests/integration
```

### 5.1 P3-000 核心 smoke 映射

| 必需闭环 | 真实测试证据 | 结果 |
|---|---|---|
| create → Running | `TestCreateSandboxEventuallyRunning` | PASS |
| argv/shell execute | `TestExecutionArgvPreservesArgumentBoundaries`、coding-agent workflow | PASS |
| explicit cancel | `TestExplicitCancelTerminatesProcessTree` | PASS |
| timeout | `TestExecutionTimeoutTerminatesProcessTree` | PASS |
| delete 与重复 delete | `TestDeleteSandboxIsIdempotentAndScoped` | PASS |
| execution/main/sidecar/runtime 清理 | `TestDeleteSandboxCleansExecutionsSidecarAndRuntime` | PASS |
| sandboxd restart | `TestSandboxdRestartRecoversRunningSandbox` | PASS |
| outbound sidecar / nft | topology、immutable CIDR policy 两项 security test | PASS |
| coding-agent clone/test/build | `TestCodingAgentLocalGitWorkflow` | PASS |

## 6. 验收中发现并解释的问题

### 6.1 coding-agent 夹具的非 root HOME

首次全量运行有 38/39 项通过，coding-agent workflow 的 `go test` 因尝试创建
`/root/.cache/go-build` 而退出。失败来自临时替代镜像没有声明适合 UID 65532 的
`HOME`，不是 MiniSandbox 的 runner、outbound 或 workspace 回归。使用同一固定基础
digest 并只增加 `HOME=/tmp` 后，该 workflow 单项通过，随后也在最终全量运行中通过。

### 6.2 WSL host sync 与 NTP 同时校时

一次全量运行中，timeout 的事件序号、`DurationMS`、TERM/KILL 和进程树清理均正确，
但 `started` 与 `timed_out` 的 wall-clock 时间发生倒退。现场证据显示：

- `systemd-timesyncd` 的 poll interval 为 32 秒；
- `timedatectl timesync-status` 报告 offset `-18.819792s`；
- Windows/WSL host sync 与 NTP 同时校时时，WSL realtime 在两个值之间跳变；
- 在单一时钟权威窗口内，timeout 三次重复测试和最终 39 项全量测试均通过。

最终验收期间临时停止 `systemd-timesyncd`，使用 Hyper-V clocksource 并与 Windows
校时；验收后已恢复原始 `tsc` 和 `systemd-timesyncd=active`。因此该失败已解释且没有
被忽略。后续在 WSL 执行依赖 wall-clock 顺序的验收时，必须先确保只有一个校时权威；
Phase 3 的 TTL/retry 实现仍应使用可注入 clock、持久化 UTC 事实和单调 duration，不能
依赖宿主 wall clock 永不回拨。

## 7. 验收后资源与状态审计

| 审计项 | 最终值 | 结果 |
|---|---:|---|
| `minisandbox.io/managed=true` 容器 | 0 | PASS |
| integration-test-id 容器 | 0 | PASS |
| managed / integration volume | 0 / 0 | PASS |
| managed / integration network | 0 / 0 | PASS |
| `sandboxd` / `runnerd` / `egressd` 宿主残留进程 | 0 / 0 / 0 | PASS |
| loopback 测试地址 | 0 | 只保留 `127.0.0.1` 与 `::1` |
| `/tmp/minisandbox-p3-000-data` | 不存在 | PASS |
| `/tmp/minisandbox-p3-000-go-cache` | 不存在 | PASS |
| 临时 coding-agent tag | 不存在 | PASS |
| integration SQLite 文件 | 0 | data root 为空后删除，无可残留的 `CLEANUP_PENDING` 记录 |

共享 `minisandbox-egress` network 在确认 `Containers={}` 后删除。临时 agent tag、测试
data root 和 Go cache 也按精确名称删除；这些临时产物不能从 Docker 受管资源中直接
恢复，但 agent 镜像可以由上节固定基础 digest 和 `HOME=/tmp` 重建。Phase 2 最终
egress 镜像和通用基础镜像按既有验收约定保留。

仓库在 P3-000 开始前已有用户所有的文档修改、WSL 维护日志/脚本和根目录构建产物；
它们不是 Docker 受管资源或运行中 execution，本任务没有修改、删除或纳入提交。
本次重新生成的 `internal/embedded/artifacts/linux_amd64/runnerd` 与 `sandbox-init` 是
被 `.gitignore` 排除且 integration 测试必需的构建产物，不构成协议或源码漂移。

## 8. 已知边界

- 当前仍以 Docker container 为隔离边界，不宣称具备 microVM 级恶意多租户隔离。
- outbound 仍是每 sandbox egress sidecar、共享 netns 和固定内部 CIDR deny；没有
  用户自定义 FQDN/CIDR/端口策略、动态规则、代理或 sidecar 自动重启。
- coding-agent 与 egress fixture 只访问隔离的本地 HTTP/Git 服务，没有把真实公网可用性
  纳入本次基线。
- SQLite schema v1、空 `expires-at` label、无 Idempotency-Key、无持久化 retry/anomaly、
  metrics/admin 默认能力尚未实现；这些正是 Phase 3 后续任务范围，不得在 P3-000 中抢跑。
- WSL 同时启用宿主校时与 NTP 时可能发生 wall-clock 回拨；这不影响已经验证的单调
  duration 和进程清理，但后续时间语义测试必须隔离该环境条件。

## 9. Phase 3 启动判定

| 门槛 | 结果 |
|---|---|
| Phase 1 验收报告可追溯且为 PASS | PASS |
| Phase 2 验收报告可追溯且为 13/13 PASS | PASS |
| 当前生产代码等于 Phase 2 被测代码 | PASS |
| schema / labels / runner / egress 版本已记录 | PASS |
| create/execute/cancel/timeout/delete/restart 真实闭环 | PASS |
| 最终 integration/security 无 FAIL、无 SKIP | PASS |
| 无 `CLEANUP_PENDING`、unknown resource 或残留 execution | PASS |
| 无公共协议或实现漂移 | PASS |

**最终判断：P3-000 完成，Phase 3 前置条件满足。下一任务只能是 P3-001，并应在
完成、聚焦测试和独立提交后暂停等待审查。**
