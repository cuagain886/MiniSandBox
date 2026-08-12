# Phase 3 手动验收手册

本文用于人工执行和复核 MiniSandbox Phase 3（可靠性与可观测性）验收。它覆盖 P3-091～P3-103，重点检查持久化事实源、崩溃恢复、TTL、幂等、并发限制、trusted orphan、Running 恢复以及管理观察面的安全语义。

本文不是生产运维手册。所有故障注入只能作用于独立测试环境以及带当前测试标签的资源，禁止对生产数据库、生产容器或来源不明的 orphan 执行。

## 1. 验收结论标准

只有同时满足以下条件，才能记录为 `PASS`：

1. 基础检查、race 检查和 Linux 构建全部通过；
2. Docker 场景在 Linux/amd64 且可访问 Docker daemon 的环境中真实执行，不能把 `SKIP` 记作通过；
3. crash suite 确实启动并强杀了外部 `sandboxd` 进程；
4. 每个测试结束后无当前测试产生的容器、volume、runtime directory 或 socket 残留；
5. Store 中不存在资源已消失但状态被错误复活、租约倒退或幂等 key 产生多个 sandbox 的情况；
6. 日志、metrics、diagnostics 和 readiness 不包含 token、命令、输出、环境变量、Docker 原始详情或宿主机敏感路径；
7. 所有命令、输出、版本、Git SHA、跳过项和人工观察均已归档。

出现以下任一情况应记录为 `FAIL`，而不是降低验收标准：

- Docker daemon 不可用、测试仅输出 `SKIP`；
- race detector 报告竞争；
- crash 测试没有命中预期 crashpoint；
- 测试结束后存在受管资源残留；
- `CLEANUP_PENDING` 无法自动恢复；
- 续期后的 sandbox 被旧 timer 删除；
- drift 被自动覆盖或歧义 orphan 被自动删除/接管；
- admin 关闭时仍注册管理路由；
- metrics scrape 直接查询 SQLite；
- 任何观察面泄露测试哨兵或 secret。

## 2. 环境要求

推荐在原生 Linux/amd64 或 WSL2 Ubuntu 中执行。Windows PowerShell 可以执行普通 Go 测试，但带 `integration` tag 的 Docker/Unix signal 场景必须在 Linux 内执行。

最低要求：

- Go 版本满足 `go.mod`，当前为 Go 1.26；
- Git、curl、jq、file、grep、base64 和支持 `pipefail` 的 Bash；
- Linux/amd64；
- Docker daemon 与当前用户可访问的 Docker socket；
- 能拉取 `debian:bookworm-slim`，或者准备一个兼容的本地测试镜像；
- 用于 integration data root 的短路径，避免 Unix Socket 路径超过限制；
- 测试期间没有其他进程修改当前仓库或测试数据目录。

先进入 Linux 工作目录。WSL 示例：

```bash
cd /mnt/e/Project/MiniSandbox
set -o pipefail
```

后续大量命令使用 `tee` 保存证据；`pipefail` 必须保持开启，否则左侧测试失败可能被 `tee` 的成功退出码掩盖。

记录环境证据：

```bash
mkdir -p .acceptance/phase3
git rev-parse HEAD | tee .acceptance/phase3/git-sha.txt
git status --short | tee .acceptance/phase3/git-status-before.txt
go version | tee .acceptance/phase3/go-version.txt
uname -a | tee .acceptance/phase3/uname.txt
docker version | tee .acceptance/phase3/docker-version.txt
docker info | tee .acceptance/phase3/docker-info.txt
```

验收前必须确认 Docker 可用：

```bash
test "$(go env GOOS)" = linux
test "$(go env GOARCH)" = amd64
docker info >/dev/null
test -S /var/run/docker.sock
```

如果 daemon 使用其他地址，后续设置：

```bash
export MINISANDBOX_TEST_DOCKER_HOST='unix:///path/to/docker.sock'
```

准备 integration 环境变量：

```bash
export MINISANDBOX_INTEGRATION=1
export MINISANDBOX_TEST_DATA_ROOT=/tmp/minisandbox-p3
export MINISANDBOX_TEST_IMAGE='debian:bookworm-slim'
export MINISANDBOX_MANUAL_DATA_ROOT=/tmp/minisandbox-p3-manual
export MINISANDBOX_MANUAL_RUN_ROOT="$MINISANDBOX_MANUAL_DATA_ROOT/run"
mkdir -p "$MINISANDBOX_TEST_DATA_ROOT"
chmod 700 "$MINISANDBOX_TEST_DATA_ROOT"
```

不要把 `MINISANDBOX_INTEGRATION` 留空；否则 Docker 测试会显示 `SKIP`。

自动化 integration harness 与人工 API 验收必须使用独立 data root，不能同时打开同一个 SQLite 文件。人工实例的构建、master key、配置和启动步骤沿用 [Phase 1 Docker 生命周期验收手册](phase1-docker-lifecycle.md)，但要把该手册中的 data、SQLite、runner socket 和 workspace 路径统一替换到 `$MINISANDBOX_MANUAL_DATA_ROOT` 下，并以当前 `configs/sandboxd.example.yaml` 的全部 Phase 3 字段为准。建议将人工实例标准输出和错误都归档：

```bash
sudo -n ./bin/sandboxd -config "$MINISANDBOX_MANUAL_DATA_ROOT/sandboxd.yaml" \
  >.acceptance/phase3/sandboxd.json.log 2>&1 &
export SANDBOXD_PID=$!
export BASE_URL=http://127.0.0.1:18080
```

启动后必须先确认 PID 存活、`/healthz` 为 `200`，且配置中的 data directory、SQLite path、runner socket directory 与 workspace directory 都位于 `$MINISANDBOX_MANUAL_DATA_ROOT`。人工验收结束时应向该 PID 发送 `SIGTERM` 并等待退出；不要用进程名批量杀死其他实例。

## 3. 基线检查

### 3.1 工作树审查

```bash
git status --short
git log -15 --oneline
git diff --check
```

验收人需要记录所有既有未提交文件。不要用 `git clean`、`git reset --hard` 或宽泛删除来准备环境。

### 3.2 全仓测试和静态检查

```bash
go test ./... 2>&1 | tee .acceptance/phase3/go-test.txt
go vet ./... 2>&1 | tee .acceptance/phase3/go-vet.txt
```

预期：命令退出码均为 0，无 `FAIL`、panic 或数据竞争报告。

### 3.3 Linux artifacts

```bash
mkdir -p .acceptance/phase3/bin
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o .acceptance/phase3/bin/runnerd ./cmd/runnerd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o .acceptance/phase3/bin/sandbox-init ./cmd/sandbox-init
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o .acceptance/phase3/bin/sandboxd ./cmd/sandboxd
file .acceptance/phase3/bin/* | tee .acceptance/phase3/linux-artifacts.txt
```

预期：三个文件均为 Linux x86-64 可执行文件。

## 4. P3-091～P3-095：外部崩溃与持久恢复

### 4.1 Crash harness 自检

```bash
go test -tags=integration -v ./tests/integration \
  -run '^TestCrashHarnessKillsAndRestartsExternalSandboxd$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-091-crash-harness.txt
```

人工确认：

- 用例不是 `SKIP`；
- integration binary 带显式 `integration` tag 构建；
- crashpoint 通过 Unix Socket 命中；
- 被测对象是外部 `sandboxd` 进程，不是 goroutine 内模拟退出；
- 同一 data directory 重启后 `/readyz` 恢复。

### 4.2 创建和删除 crash matrix

```bash
go test -tags=integration -v ./tests/integration \
  -run '^(TestCreateCrashPointMatrix|TestDeleteCrashPointMatrix)$' \
  -count=1 -timeout=30m 2>&1 | tee .acceptance/phase3/p3-094-095-crash-matrix.txt
```

创建矩阵应覆盖 Store commit、runtime directory、workspace volume、container、artifact copy、container start、runner ready 和 Running CAS 前后。删除矩阵应覆盖终止意图、runner shutdown、各 runtime 删除阶段以及 Terminated CAS 前后。

预期：

- 每个 crashpoint 至少执行两轮；
- 每轮只存在一个 sandbox 记录和一套受管资源；
- 重启后 create 最终为 `Running`；
- delete 最终为 `Terminated` 且资源数为零。

### 4.3 丢失 wake 和持久 retry

```bash
go test -tags=integration -v ./tests/integration \
  -run '^TestPeriodicScannerRecoversDroppedCreateAndDeleteWake$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-092-wake-recovery.txt

go test -v ./internal/reconcile \
  -run '^TestRetryScheduleSurvivesSQLiteRestartAndRecoversWithoutManualWake$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-093-retry-restart.txt
```

预期：wake 丢失不改变 Store 事实；scanner 或重启恢复能自动发现记录；持久化的 `attempt` 和绝对 `next_reconcile_at` 在重启前后保持一致。

## 5. P3-096：CLEANUP_PENDING 自动恢复

先执行确定性矩阵：

```bash
go test -v ./internal/reconcile \
  -run '^TestCleanupPendingAutomaticallyRecoversFromPersistedSchedule$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-096-cleanup-pending.txt
```

再执行真实 Docker workspace blocker：

```bash
go test -tags=integration -v ./tests/integration \
  -run '^TestCleanupPendingCanResumeAfterBlockerRemoval$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-096-cleanup-docker.txt
```

人工观察点：

1. 第一次 DELETE 返回 `202`；
2. 主容器已删除，但被 blocker 占用的 workspace volume 保留；
3. API 状态为 `Failed`，reason 为 `CLEANUP_PENDING`；
4. blocker 删除后，重试或周期扫描进入普通 reconcile；
5. 最终为 `Terminated`，container、volume 和 runtime directory 均不存在；
6. retry attempt 清零，`next_reconcile_at` 清空。

## 6. P3-097：TTL、renew、旧 timer 与重启

```bash
go test -v ./internal/application ./internal/reconcile \
  -run 'TestRenewSandbox|TestRenewedLeaseSurvivesStaleTimerHeapLossAndRestart|TestTTLRecovery|TestTTLExpiration' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-097-ttl-renew.txt
```

人工 API 快速检查可使用独立测试实例。创建：

```bash
IDEMPOTENCY_KEY="manual-ttl-$(date +%s)"
curl -i -sS -X POST "$BASE_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data '{"image":"debian:bookworm-slim","ttl_seconds":120}' \
  | tee .acceptance/phase3/manual-create-response.txt
```

从响应正文记录 `id` 和原始 `expires_at`，然后延长绝对租约：

```bash
SANDBOX_ID='<create 返回的 id>'
NEW_EXPIRY='<晚于当前 expires_at、且位于服务端 TTL 上限内的 RFC3339 UTC 时间>'
curl -i -sS -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/renew" \
  -H 'Content-Type: application/json' \
  --data "{\"expires_at\":\"$NEW_EXPIRY\"}"
```

检查项：

- 相同 expiry 返回 `200` 且 revision 不增加；
- 延长返回 `200`，Store expiry 与 `lease.json` 最终更新；
- 缩短、已经到期、已提交删除意图均返回 `409`；
- 旧 timer 触发时 sandbox 仍为 `Running`；
- 新 expiry 到达后最终为 `Terminated`；
- 重启和 heap 丢失后仍以 SQLite 最新 expiry 为准；
- v1 且无 manifest 的 orphan 不根据不可信 TTL 自动删除。

`lease.json` 只能读取验证，禁止手工改写：

```bash
sudo jq . "$MINISANDBOX_MANUAL_RUN_ROOT/$SANDBOX_ID/lease.json"
```

## 7. P3-098：生命周期并发与 race

```bash
go test -race -v ./internal/application ./internal/reconcile \
  -run '^(TestRenewDeleteRaceNeverRegressesExpiryOrResurrects|TestConcurrentRenewKeepsMaximumExpiry|TestTTLHeapConcurrentAccess)$' \
  -count=10 2>&1 | tee .acceptance/phase3/p3-098-lifecycle-race.txt
```

预期：

- 无 race report；
- renew/renew 最终保存最大已接受 expiry；
- renew/delete 最终删除意图占优；
- `Terminated` 不会恢复为 `Running`；
- 重复 scanner 不产生重复资源；
- revision 冲突不会让 expiry 倒退。

## 8. P3-099：Idempotency 并发与响应丢失

```bash
go test -race -v ./internal/store/sqlite \
  -run '^TestCreateIdempotentConcurrent' \
  -count=10 2>&1 | tee .acceptance/phase3/p3-099-idempotency-race.txt

go test -tags=integration -v ./tests/integration \
  -run '^TestIdempotencyReplaysCommittedCreateAfterLostResponse$' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-099-lost-response.txt
```

人工 API 检查：

```bash
KEY="manual-idempotency-$(date +%s)"
BODY='{"image":"debian:bookworm-slim","ttl_seconds":600}'
curl -sS -D .acceptance/phase3/idem-first.headers \
  -o .acceptance/phase3/idem-first.json \
  -X POST "$BASE_URL/v1/sandboxes" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $KEY" --data "$BODY"
curl -sS -D .acceptance/phase3/idem-replay.headers \
  -o .acceptance/phase3/idem-replay.json \
  -X POST "$BASE_URL/v1/sandboxes" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $KEY" --data "$BODY"
diff -u .acceptance/phase3/idem-first.json .acceptance/phase3/idem-replay.json
```

预期：两个响应均为 `202`，`Location` 和安全响应正文相同，只有每次请求生成的 `X-Request-ID` 可以不同。使用同 key 但不同 image/TTL/outbound 时必须返回 `409`。满额时已有身份 replay 仍成功；24 小时终态保留期内仍 replay，GC 后 key 才能复用。

## 9. P3-100：Quota 与 Operation 限制

```bash
go test -race -v ./internal/runtime ./internal/runtime/docker ./internal/reconcile ./internal/store/sqlite \
  -run 'TestLifecycleOperationLimitersRespectIndependentConfiguredPeaks|TestOperationLimiterFailureCancelAndRestartDoNotLeakSlots|TestSandboxQuotaConcurrentDeleteReleaseAndReplay|TestCreateLimiter|TestDeleteLimiter|TestEnsureImagePullLimiter' \
  -count=10 2>&1 | tee .acceptance/phase3/p3-100-limits.txt
```

预期：

- `max_sandboxes` 的 active Store count 从不超限；
- 满额 replay 不占第二份容量；
- `Terminated` 后只释放一份 admission；
- create、image pull、delete 的实际峰值分别不超过各自配置；
- 等待取消、runtime failure、panic 和重启不泄漏 slot；
- 三类 limiter 互相独立。

## 10. P3-101：Trusted Orphan 与 Anomaly

执行完整策略矩阵：

```bash
go test -v ./internal/reconcile ./internal/store/sqlite \
  -run 'TestTrustedOrphanAndAnomalyPolicyWithSQLite|TestOrphanImportExecutor|TestFinalizeAnomalyScan|TestRuntimeAnomaly' \
  -count=1 2>&1 | tee .acceptance/phase3/p3-101-orphan-anomaly.txt
```

矩阵必须覆盖：

| Fixture | 预期行为 |
|---|---|
| v2 完整可信 bundle | 原子导入一次，进入普通 reconcile |
| manifest 中更新后的 expiry | 优先于创建时 immutable label |
| 明确已过期的可信 bundle | 导入 `DesiredTerminated`，走 normal delete |
| v1 且无 manifest | 保留资源并记录 anomaly |
| 未知 schema | 保留并记录 `unknown_schema` |
| spec hash mismatch | 保留并记录 `spec_hash_mismatch` |
| main/sidecar partial | 保留并记录 incomplete/network anomaly |
| 仅 workspace volume | 保留并记录 incomplete bundle |
| symlink/危险 runtime directory | 不跟随、不导入，记录安全 anomaly |

人工复核：

- 重复 recovery 不产生第二条 sandbox 记录；
- 不可信资源没有被停止、删除、改 label 或接管；
- partial inventory 不能 resolve 旧 anomaly；
- 只有 container、volume 和 filesystem 三类 inventory 全部成功，且资源确实消失时，anomaly 才标记 resolved；
- 全程只使用测试 harness 的随机 test label，不操作其他 Docker 资源。

## 11. P3-102：Running Drift 与 Runner 恢复

```bash
go test -race -v ./internal/reconcile \
  -run 'TestRunningRecoveryWithSQLitePreservesWorkspaceAndLease|TestRunningSpecDriftFailsClosedWithoutReplacement|TestRecoverRunning' \
  -count=10 2>&1 | tee .acceptance/phase3/p3-102-running-recovery.txt
```

真实 Docker 人工观察建议使用 integration harness 创建的 sandbox，只对 `minisandbox.io/id=<SANDBOX_ID>` 精确匹配的 main/sidecar 操作。不得按名称前缀批量停止或删除。

查找当前 main：

```bash
docker ps -a --filter "label=minisandbox.io/id=$SANDBOX_ID" \
  --filter 'label=minisandbox.io/managed=true' \
  --format '{{.ID}} {{.Names}} {{.Status}}'
```

在 workspace 写哨兵并记录当前租约：

```bash
curl -sS -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions" \
  -H 'Content-Type: application/json' \
  --data '{"argv":["sh","-c","printf workspace-sentinel > /workspace/p3-sentinel"]}'
sudo cp "$MINISANDBOX_MANUAL_RUN_ROOT/$SANDBOX_ID/lease.json" ".acceptance/phase3/$SANDBOX_ID.lease.before.json"
```

然后只停止精确 main container，等待周期 health/reconcile：

```bash
MAIN_ID='<上一步精确匹配得到的 main container ID>'
docker stop "$MAIN_ID"
```

预期：

- network=none 的 missing/stopped main 使用 Ensure 恢复；
- outbound 的 main/sidecar/egress gate 故障使用聚合 `ReplaceCompute`；
- 前两次 runner probe failure 只增加 health count，第三次才替换 compute；
- ReplaceCompute 不调用完整 Delete，不删除 workspace volume 和 `lease.json`；
- 恢复后 compute 资源唯一，workspace 哨兵仍存在，lease 是 Store 最新版本；
- replacement 前并发 DELETE 时，删除意图优先；
- spec hash drift 最终保持 `Failed/SPEC_DRIFT`，不会被 Ensure/ReplaceCompute 自动覆盖；
- 不要求恢复进行中的 execution 或 workspace 以外的数据。

## 12. P3-103：Logs、Metrics、Diagnostics 与 Readiness

先执行组合安全验收：

```bash
go test -race -v ./internal/api ./internal/application ./internal/observability/... \
  -run 'TestObservabilitySurfacesShareSafeSemantics|TestSnapshotGaugesRetainLastSuccessAndScrapeDoesNotQueryStore|TestAdminRoutesAreAbsentUnlessExplicitlyWired|TestReadinessEndpointStates|TestLoggerWritesStableJSONFields' \
  -count=10 2>&1 | tee .acceptance/phase3/p3-103-observability.txt
```

### 12.1 Admin 默认关闭

保持配置：

```yaml
admin:
  enabled: false
  token_file: "/path/that/must/not/be/read"
```

检查：

```bash
curl -i -sS "$BASE_URL/metrics"
curl -i -sS "$BASE_URL/v1/admin/diagnostics"
```

预期：两个路径自然返回 `404`，启动不读取 `token_file`。当前服务实现提供聚合 `/v1/admin/diagnostics`；如验收目标按 `api/admin.openapi.yaml` 的单 sandbox 路径执行，应先解决契约与实现路径差异，不能把其中一个结果冒充另一个。

### 12.2 Admin 启用与鉴权

生成独立测试 token 文件，禁止把 raw token 写入 YAML。文件内容是无 padding 的 base64url 文本，解码后必须恰好为 32 字节：

```bash
ADMIN_TOKEN="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"
printf '%s' "$ADMIN_TOKEN" > /tmp/minisandbox-admin.token
chmod 600 /tmp/minisandbox-admin.token
```

配置引用：

```yaml
admin:
  enabled: true
  token_file: "/tmp/minisandbox-admin.token"
```

检查未鉴权与正确鉴权：

```bash
curl -i -sS "$BASE_URL/metrics"
curl -i -sS "$BASE_URL/v1/admin/diagnostics"
curl -i -sS -H "Authorization: Bearer $ADMIN_TOKEN" "$BASE_URL/metrics" \
  | tee .acceptance/phase3/metrics.txt
curl -i -sS -H "Authorization: Bearer $ADMIN_TOKEN" "$BASE_URL/v1/admin/diagnostics" \
  | tee .acceptance/phase3/diagnostics.json
```

预期：无 token、错误 token 和重复 Authorization 均统一返回 `401`；正确 token 返回 `200`。

### 12.3 Metrics 人工检查

```bash
grep '^minisandbox_' .acceptance/phase3/metrics.txt | sort
grep -E 'sandbox_id|execution_id|request_id|token|command|output|environment|container_id|socket|host_path' \
  .acceptance/phase3/metrics.txt && echo 'FAIL: high-cardinality or secret label found'
grep 'minisandbox_execution_total' .acceptance/phase3/metrics.txt && echo 'FAIL: ambiguous metric found'
```

必须能看到相关场景的固定指标，例如：

- `minisandbox_sandbox_create_requests_total`；
- `minisandbox_reconcile_total`；
- `minisandbox_retry_scheduled_total`；
- `minisandbox_lease_expired_total`；
- `minisandbox_orphan_observations_total`；
- `minisandbox_runtime_docker_operations_total`；
- `minisandbox_execution_requests_total`；
- `minisandbox_execution_foreground_terminal_observed_total`；
- `minisandbox_metrics_snapshot_age_seconds`。

连续 scrape 不应触发 SQLite 查询；Store sampler 失败时保留上次成功 snapshot，并通过 snapshot age 表达陈旧度。

### 12.4 Diagnostics 和 readiness

diagnostics 只能包含固定 section、状态、计数和 anomaly 分类。不得出现 raw Docker inspect、container logs、错误 cause、命令、输出、环境、token、socket 或宿主机路径。

```bash
curl -sS "$BASE_URL/readyz" | tee .acceptance/phase3/ready-before.json
```

在独立测试环境暂停 Docker daemon 或让配置的 Docker endpoint 暂时不可达，等待超过 freshness 窗口，再执行：

```bash
curl -i -sS "$BASE_URL/readyz" | tee .acceptance/phase3/ready-degraded.txt
```

预期 `/readyz` 为 `503`，仅指出固定 `docker: not_ready`，不回显 socket 或错误详情；`/healthz` 仍为 `200`。恢复 Docker 后，等待 probe 成功，`/readyz` 应回到 `200`。不要在共享 Docker daemon 上执行该步骤。

### 12.5 日志哨兵检查

用只属于测试环境的哨兵作为 idempotency key、环境变量、命令参数和输出：

```bash
SENTINEL='P3_SECRET_SENTINEL_DO_NOT_LOG'
```

执行正常、retry、TTL、orphan 和 Docker degrade/recover 场景后，只扫描本次测试实例捕获的 JSON 日志：

```bash
grep -F "$SENTINEL" .acceptance/phase3/sandboxd.json.log && echo 'FAIL: sentinel leaked'
grep -F "$ADMIN_TOKEN" .acceptance/phase3/sandboxd.json.log && echo 'FAIL: admin token leaked'
```

允许的日志字段是固定机器值和安全 ID，例如 `component`、`request_id`、`sandbox_id`、`operation`、`attempt`、`duration_ms`、`delay_ms`、`result`、`error_code`、`error_class` 和安全 anomaly 分类。禁止记录 raw error、完整 fingerprint、key 值或用户内容。

## 13. 全套 race 与 Docker 回归

在所有聚焦场景通过后执行：

```bash
go test -race ./internal/application/... ./internal/reconcile/... ./internal/runtime/... ./internal/store/sqlite/... ./internal/api/... ./internal/observability/... \
  2>&1 | tee .acceptance/phase3/race-all.txt

go test -tags=integration -v ./tests/integration/... -count=1 -timeout=60m \
  2>&1 | tee .acceptance/phase3/docker-integration-all.txt
```

检查输出中没有跳过关键可靠性场景：

```bash
grep -E -- '--- SKIP: (TestCrash|TestCreateCrash|TestDeleteCrash|TestCleanupPending|TestPeriodic|TestIdempotency)' \
  .acceptance/phase3/docker-integration-all.txt && echo 'FAIL: required integration test skipped'
```

## 14. 资源清理与残留审计

integration harness 会按随机 `io.minisandbox.integration-test-id` 标签清理自己的资源。测试结束后仍需只读审计：

```bash
docker ps -a --filter 'label=io.minisandbox.integration-test-id' \
  --format '{{.ID}} {{.Names}} {{.Labels}}' \
  | tee .acceptance/phase3/remaining-test-containers.txt
docker volume ls --filter 'label=io.minisandbox.integration-test-id' \
  --format '{{.Name}} {{.Labels}}' \
  | tee .acceptance/phase3/remaining-test-volumes.txt
find "$MINISANDBOX_TEST_DATA_ROOT" -mindepth 1 -maxdepth 2 -print \
  | tee .acceptance/phase3/remaining-test-files.txt
```

三个结果都应为空。若不为空，先记录完整证据，再只按精确 test ID 清理；禁止使用宽泛名称、前缀、`docker system prune` 或递归删除未知目录。

清理人工 API 创建的 sandbox：

```bash
curl -i -sS -X DELETE "$BASE_URL/v1/sandboxes/$SANDBOX_ID"
```

等待 API 为 `Terminated` 后，再确认：

```bash
docker ps -a --filter "label=minisandbox.io/id=$SANDBOX_ID"
docker volume ls --filter "label=minisandbox.io/id=$SANDBOX_ID"
test ! -e "$MINISANDBOX_MANUAL_RUN_ROOT/$SANDBOX_ID"
```

## 15. 验收记录模板

复制以下表格到验收报告并填写真实证据，不得预填 `PASS`：

| 项目 | 结果 | 命令/证据文件 | 备注 |
|---|---|---|---|
| Git SHA 与工作树 |  | `git-sha.txt`、`git-status-before.txt` |  |
| `go test ./...` |  | `go-test.txt` |  |
| `go vet ./...` |  | `go-vet.txt` |  |
| Linux artifacts |  | `linux-artifacts.txt` |  |
| P3-091 crash harness |  | `p3-091-crash-harness.txt` | 不得 SKIP |
| P3-092 wake recovery |  | `p3-092-wake-recovery.txt` |  |
| P3-093 retry restart |  | `p3-093-retry-restart.txt` |  |
| P3-094/095 crash matrix |  | `p3-094-095-crash-matrix.txt` |  |
| P3-096 cleanup pending |  | `p3-096-*.txt` |  |
| P3-097 TTL/renew |  | `p3-097-ttl-renew.txt` |  |
| P3-098 lifecycle race |  | `p3-098-lifecycle-race.txt` |  |
| P3-099 idempotency |  | `p3-099-*.txt` |  |
| P3-100 limits |  | `p3-100-limits.txt` |  |
| P3-101 orphan/anomaly |  | `p3-101-orphan-anomaly.txt` |  |
| P3-102 Running recovery |  | `p3-102-running-recovery.txt` |  |
| P3-103 observability |  | `p3-103-observability.txt` |  |
| 全套 race |  | `race-all.txt` |  |
| Docker integration |  | `docker-integration-all.txt` | 不得关键 SKIP |
| 容器/volume/文件残留 |  | `remaining-test-*.txt` | 必须为空 |
| 最终结论 |  |  | PASS / FAIL |

最终报告至少记录：Git SHA、OS/arch、Go/Docker 版本、Docker endpoint 类型、测试镜像精确引用、配置差异、全部命令退出码、crashpoint 列表、race 结果、残留审计、跳过理由和已知限制。
