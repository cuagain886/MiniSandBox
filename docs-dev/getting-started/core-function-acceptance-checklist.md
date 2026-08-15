# MiniSandbox 核心功能验收清单与执行步骤

本文用于在独立 Linux/amd64 + Docker 环境中验收 MiniSandbox 当前核心功能。它既提供逐项清单，也提供可以直接执行的命令、证据文件和 PASS/FAIL 判定标准。

本文不是生产运维手册。所有测试、进程强杀和 Docker 资源操作必须发生在专用验收环境中，并且只能作用于本次验收创建的进程和带精确 sandbox ID/test ID label 的资源。

## 1. 验收目标与结论规则

核心验收覆盖：

- 仓库基线、静态检查和 Linux 构建；
- 控制面启动、health 与 readiness；
- sandbox 创建、查询、幂等重放和删除；
- 前台 SSE、后台执行、日志和取消；
- 非 root、capability、cwd、socket、环境和秘密隔离；
- TTL 自动到期、renew 和旧 timer 安全；
- 周期 reconcile、retry、cleanup pending 和崩溃恢复；
- admin 默认关闭、鉴权 metrics、diagnostics 和安全日志；
- outbound sidecar、network namespace、nft 内部 CIDR 阻断；
- 容器、volume、runtime directory 和进程残留清理。

每一项只能记录为以下状态之一：

| 状态 | 含义 |
|---|---|
| `PASS` | 命令真实执行、退出码为 0，且人工观察符合全部预期 |
| `FAIL` | 功能、状态、资源、安全语义或证据不符合预期 |
| `BLOCKED` | 环境或必要 artifact 缺失，测试没有真实执行 |
| `N/A` | 仅用于本次明确不声明支持的非核心能力，必须写明理由 |

完整项目核心功能只能在 C00～C12 全部 `PASS` 时记为 `PASS`。Docker 不可用、integration test `SKIP`、缺少 egress digest 或只运行单元测试都不能替代真实验收。

## 2. 当前已知验收阻塞项

截至本文创建时，当前仓库执行 `go test ./...` 会在 `tests/contract` 失败：

- [phase2_documentation_test.go](../../tests/contract/phase2_documentation_test.go) 仍读取 `docs/phase-2-operations-guide.md`；
- 实际开发文档已经位于 `docs-dev/dev/phase-2-operations-guide.md`；
- 当前 README 也没有 contract test 要求的旧路径链接。

这是确定性的契约回归，不能忽略或把其余包通过等同于全仓通过。正式验收前需要统一文档位置、README 链接和 contract test，然后重新执行 C00。

另一个必须记录的问题是 Admin diagnostics 路径存在契约漂移：

- `api/admin.openapi.yaml` 定义 `/v1/admin/sandboxes/{sandbox_id}/diagnostics`；
- 当前 router 实现 `/v1/admin/diagnostics`。

C10 可以按当前实现验证安全行为，但最终发布验收必须先拍板接口语义并同步 OpenAPI、handler 和 contract tests。

## 3. 环境要求与安全约束

### 3.1 必需环境

- 原生 Linux/amd64 或 WSL2 Linux/amd64；
- Go 版本满足 `go.mod`，当前为 Go 1.26；
- GNU Make、Git、curl、jq、file、grep、sed、base64；
- 当前用户可以访问 Docker Engine；
- 可以获得 `debian:bookworm-slim`；
- 完整 outbound 验收还需要已批准的 `repository@sha256:<digest>` egress 镜像。

Windows PowerShell 单测不能替代 Linux signal、Unix Socket、PID 1、network namespace 和 nftables 验收。

### 3.2 禁止事项

- 不得在生产 Docker daemon 上执行 crash 或资源删除测试；
- 不得执行 `docker system prune`；
- 不得按 `minisandbox-*` 名称前缀批量删除资源；
- 不得递归删除来源不明的 data directory；
- 不得把 runner master key、admin token 或用户秘密写入 Git、Docker labels 或验收报告；
- 不得把 `SKIP`、未运行或仅 Windows 通过记录成 `PASS`。

## 4. 准备验收环境和证据目录

进入仓库并开启严格 Bash 语义：

```bash
cd /path/to/MiniSandbox
set -euo pipefail
```

创建每次唯一的验收目录：

```bash
export ACCEPT_RUN="$(date -u +%Y%m%dT%H%M%SZ)-$(git rev-parse --short=12 HEAD)"
export MS_ROOT="/tmp/minisandbox-core-$ACCEPT_RUN"
export EVIDENCE_ROOT="$MS_ROOT/evidence"
export MS_CONFIG="$MS_ROOT/sandboxd.yaml"
export MS_RUN_ROOT="$MS_ROOT/run"
export BASE_URL="http://127.0.0.1:18080"
install -d -m 0700 "$MS_ROOT" "$EVIDENCE_ROOT"
```

记录环境事实：

```bash
git rev-parse HEAD | tee "$EVIDENCE_ROOT/git-sha.txt"
git status --short | tee "$EVIDENCE_ROOT/git-status.txt"
go version | tee "$EVIDENCE_ROOT/go-version.txt"
uname -a | tee "$EVIDENCE_ROOT/uname.txt"
docker version | tee "$EVIDENCE_ROOT/docker-version.txt"
docker info | tee "$EVIDENCE_ROOT/docker-info.txt"
```

必须确认：

```bash
test "$(go env GOOS)" = linux
test "$(go env GOARCH)" = amd64
docker info >/dev/null
```

如果 Docker daemon 使用非默认地址，设置：

```bash
export MINISANDBOX_TEST_DOCKER_HOST='unix:///path/to/docker.sock'
```

## 5. C00：仓库基线与契约检查

### 清单

- [ ] 验收 SHA 已记录；
- [ ] 工作树状态已记录并解释；
- [ ] `go test ./...` 通过；
- [ ] `go vet ./...` 通过；
- [ ] `git diff --check` 通过；
- [ ] OpenAPI、protocol、SDK 和文档 contract tests 无漂移。

### 步骤

```bash
git diff --check 2>&1 | tee "$EVIDENCE_ROOT/git-diff-check.txt"
go test ./... 2>&1 | tee "$EVIDENCE_ROOT/go-test-all.txt"
go vet ./... 2>&1 | tee "$EVIDENCE_ROOT/go-vet.txt"
```

### PASS 标准

- 三个命令退出码均为 0；
- 无 `FAIL`、panic 或缺失文档；
- 不能使用 `-run`、`|| true` 或删除失败测试规避全仓回归。

当前已知的 Phase 2 文档路径失败未解决前，本项必须记为 `FAIL`。

## 6. C01：Linux 构建与 Artifact

### 清单

- [ ] `sandboxd` 构建成功；
- [ ] `runnerd` 构建成功；
- [ ] `sandbox-init` 构建成功；
- [ ] `egressd` 构建成功；
- [ ] 注入容器的 runner/init 是 Linux x86-64 artifact。

### 步骤

```bash
make build 2>&1 | tee "$EVIDENCE_ROOT/make-build.txt"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -o "$EVIDENCE_ROOT/egressd" ./cmd/egressd
file bin/sandboxd bin/runnerd bin/sandbox-init "$EVIDENCE_ROOT/egressd" \
  | tee "$EVIDENCE_ROOT/artifact-file-types.txt"
```

### PASS 标准

- 命令退出码为 0；
- 四个可执行文件均为 Linux x86-64；
- 不能用仓库根目录中的历史散落二进制替代本次构建结果。

## 7. C02：独立控制面启动、Health 与 Readiness

### 7.1 准备独立配置

复制当前示例配置并替换成本次专用路径和端口：

```bash
cp configs/sandboxd.example.yaml "$MS_CONFIG"
sed -i \
  -e 's#127.0.0.1:8080#127.0.0.1:18080#' \
  -e "s#/var/lib/minisandbox#$MS_ROOT#g" \
  -e "s#/etc/minisandbox/runner-master-key#$MS_ROOT/runner-master-key#" \
  "$MS_CONFIG"
umask 077
head -c 32 /dev/urandom > "$MS_ROOT/runner-master-key"
chmod 600 "$MS_ROOT/runner-master-key"
```

确认配置中的以下路径都位于 `$MS_ROOT`：

- `data.directory`；
- `data.sqlite_path`；
- `runtime.runner_socket_directory`；
- `runtime.workspace_directory`；
- `security.runner_master_key_file`。

### 7.2 启动服务

```bash
./bin/sandboxd -config "$MS_CONFIG" \
  >"$EVIDENCE_ROOT/sandboxd.log" 2>&1 &
export SANDBOXD_PID=$!
trap 'kill -TERM "$SANDBOXD_PID" 2>/dev/null || true' EXIT
```

等待就绪：

```bash
READY=false
for attempt in $(seq 1 60); do
  if curl -fsS --max-time 2 "$BASE_URL/readyz" \
      >"$EVIDENCE_ROOT/readyz.json"; then
    READY=true
    break
  fi
  kill -0 "$SANDBOXD_PID"
  sleep 1
done
test "$READY" = true
curl -fsS "$BASE_URL/healthz" | tee "$EVIDENCE_ROOT/healthz.json"
jq . "$EVIDENCE_ROOT/readyz.json"
```

### PASS 标准

- 进程保持存活；
- `/healthz` 返回 `200`；
- `/readyz` 在 60 秒内返回 `200`；
- readiness JSON 不包含 Docker socket、宿主机路径或 raw error；
- 日志中没有 master key 或配置中的敏感路径内容。

## 8. C03：创建、查询与 Idempotency-Key

### 8.1 首次创建

```bash
export IDEMPOTENCY_KEY="core-$ACCEPT_RUN"
export CREATE_BODY='{"image":"debian:bookworm-slim","ttl_seconds":600}'

curl -sS -D "$EVIDENCE_ROOT/create-first.headers" \
  -o "$EVIDENCE_ROOT/create-first.json" \
  -w '%{http_code}' \
  -X POST "$BASE_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data "$CREATE_BODY" \
  | tee "$EVIDENCE_ROOT/create-first.status"

test "$(cat "$EVIDENCE_ROOT/create-first.status")" = 202
export SANDBOX_ID="$(jq -er '.id' "$EVIDENCE_ROOT/create-first.json")"
test -n "$SANDBOX_ID"
```

### 8.2 等待 Running

```bash
RUNNING=false
for attempt in $(seq 1 90); do
  curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID" \
    >"$EVIDENCE_ROOT/sandbox-current.json"
  STATE="$(jq -r '.state' "$EVIDENCE_ROOT/sandbox-current.json")"
  if [ "$STATE" = Running ]; then
    RUNNING=true
    break
  fi
  case "$STATE" in Failed|Terminated) jq . "$EVIDENCE_ROOT/sandbox-current.json"; exit 1;; esac
  sleep 1
done
test "$RUNNING" = true
jq . "$EVIDENCE_ROOT/sandbox-current.json"
```

### 8.3 相同请求重放

```bash
curl -sS -D "$EVIDENCE_ROOT/create-replay.headers" \
  -o "$EVIDENCE_ROOT/create-replay.json" \
  -w '%{http_code}' \
  -X POST "$BASE_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data "$CREATE_BODY" \
  | tee "$EVIDENCE_ROOT/create-replay.status"

test "$(cat "$EVIDENCE_ROOT/create-replay.status")" = 202
cmp "$EVIDENCE_ROOT/create-first.json" "$EVIDENCE_ROOT/create-replay.json"
```

人工比较两个 header：`Location` 必须相同，`X-Request-ID` 必须存在且每次请求不同。

同 key、不同请求必须冲突：

```bash
curl -sS -o "$EVIDENCE_ROOT/create-conflict.json" \
  -w '%{http_code}' \
  -X POST "$BASE_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data '{"image":"debian:bookworm-slim","ttl_seconds":601}' \
  | tee "$EVIDENCE_ROOT/create-conflict.status"
test "$(cat "$EVIDENCE_ROOT/create-conflict.status")" = 409
```

### PASS 标准

- 首次创建和 replay 都返回 `202`；
- 两次返回相同 sandbox ID、Location 和安全响应正文；
- 同 key 不同 request 返回 `409`；
- 最终只有一套带该 sandbox ID label 的受管资源。

## 9. C04：前台执行与 SSE

执行同时产生 stdout、stderr 和非零退出码的命令：

```bash
curl -sS -N -D "$EVIDENCE_ROOT/foreground.headers" \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data '{"argv":["sh","-c","printf stdout-marker; printf stderr-marker >&2; exit 7"]}' \
  | tee "$EVIDENCE_ROOT/foreground.sse"
```

### 清单

- [ ] HTTP content type 是 `text/event-stream`；
- [ ] 首个事件是 `started`；
- [ ] stdout 和 stderr 是不同事件类型；
- [ ] sequence 从 1 开始并严格递增；
- [ ] timestamp 为服务端 UTC 时间；
- [ ] 最终只有一个 `exited`；
- [ ] `exit_code` 为 7，非零退出没有被错误映射为 transport failure。

SSE 中用户输出使用协议编码时，按协议字段解码后必须分别得到 `stdout-marker` 和 `stderr-marker`。

## 10. C05：后台执行、日志与取消

创建后台任务：

```bash
curl -fsS -D "$EVIDENCE_ROOT/background.headers" \
  -o "$EVIDENCE_ROOT/background.json" \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions" \
  -H 'Content-Type: application/json' \
  --data '{"shell":"echo background-started; sleep 300","background":true}'
export EXECUTION_ID="$(jq -er '.execution_id' "$EVIDENCE_ROOT/background.json")"
```

查询状态和第一页日志：

```bash
curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions/$EXECUTION_ID" \
  | tee "$EVIDENCE_ROOT/background-status-before.json"
curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions/$EXECUTION_ID/logs?cursor=0" \
  | tee "$EVIDENCE_ROOT/background-logs-first.json"
```

取消并等待终态：

```bash
curl -sS -o /dev/null -w '%{http_code}' \
  -X DELETE "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions/$EXECUTION_ID" \
  | tee "$EVIDENCE_ROOT/background-cancel.status"

CANCELLED=false
for attempt in $(seq 1 30); do
  curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions/$EXECUTION_ID" \
    >"$EVIDENCE_ROOT/background-status-after.json"
  STATE="$(jq -r '.state' "$EVIDENCE_ROOT/background-status-after.json")"
  if [ "$STATE" = Cancelled ]; then CANCELLED=true; break; fi
  sleep 1
done
test "$CANCELLED" = true
```

### PASS 标准

- 创建返回后台 execution descriptor 和稳定 execution ID；
- 日志页包含单调 sequence、`next_cursor` 和 `complete`；
- cancel 返回 `202` 或已终态时的 `204`；
- 最终状态为 `Cancelled`，且只有一个 terminal event；
- `sleep` 及其后代进程没有残留。

完整进程树、断开语义和输出排空由 C08 的 Docker integration tests 强制验证。

## 11. C06：执行身份与安全边界

人工检查执行 UID/GID 和有效 capability：

```bash
curl -sS -N \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data '{"argv":["sh","-c","id -u; id -g; grep ^CapEff: /proc/self/status"]}' \
  | tee "$EVIDENCE_ROOT/execution-identity.sse"
```

预期解码后的输出包含 UID `1000`、GID `1000`，`CapEff` 全零。

检查主容器安全配置，只使用精确 labels：

```bash
docker ps -a \
  --filter "label=minisandbox.io/id=$SANDBOX_ID" \
  --filter 'label=minisandbox.io/resource-role=main' \
  --format '{{.ID}}' \
  | tee "$EVIDENCE_ROOT/main-container-id.txt"
export MAIN_ID="$(cat "$EVIDENCE_ROOT/main-container-id.txt")"
test -n "$MAIN_ID"
docker inspect "$MAIN_ID" > "$EVIDENCE_ROOT/main-inspect.json"
```

### 清单

- [ ] `Privileged=false`；
- [ ] `CapDrop` 包含 `ALL`，只回加固定 init/bootstrap 必需能力；
- [ ] `no-new-privileges` 已启用；
- [ ] 未发布端口；
- [ ] 未挂载 Docker socket、宿主机凭据或任意用户 host path；
- [ ] CPU、memory 和 PIDs 限制存在；
- [ ] 默认 sandbox 没有外部网络；
- [ ] 用户 execution 无 root、无 capability；
- [ ] labels 不包含 token、命令、环境或输出。

## 12. C07：TTL、Renew 与自动到期

### 12.1 续期现有 sandbox

```bash
export NEW_EXPIRY="$(date -u -d '+20 minutes' +%Y-%m-%dT%H:%M:%SZ)"
curl -fsS -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/renew" \
  -H 'Content-Type: application/json' \
  --data "{\"expires_at\":\"$NEW_EXPIRY\"}" \
  | tee "$EVIDENCE_ROOT/renew-extended.json"
test "$(jq -r '.expires_at' "$EVIDENCE_ROOT/renew-extended.json")" = "$NEW_EXPIRY"
```

相同 expiry 必须是 `200` no-op：

```bash
curl -sS -o "$EVIDENCE_ROOT/renew-equal.json" -w '%{http_code}' \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/renew" \
  -H 'Content-Type: application/json' \
  --data "{\"expires_at\":\"$NEW_EXPIRY\"}" \
  | tee "$EVIDENCE_ROOT/renew-equal.status"
test "$(cat "$EVIDENCE_ROOT/renew-equal.status")" = 200
```

缩短租约必须返回 `409`：

```bash
SHORT_EXPIRY="$(date -u -d '+5 minutes' +%Y-%m-%dT%H:%M:%SZ)"
curl -sS -o "$EVIDENCE_ROOT/renew-shorter.json" -w '%{http_code}' \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/renew" \
  -H 'Content-Type: application/json' \
  --data "{\"expires_at\":\"$SHORT_EXPIRY\"}" \
  | tee "$EVIDENCE_ROOT/renew-shorter.status"
test "$(cat "$EVIDENCE_ROOT/renew-shorter.status")" = 409
```

确认 runtime lease projection：

```bash
jq . "$MS_RUN_ROOT/$SANDBOX_ID/lease.json" \
  | tee "$EVIDENCE_ROOT/lease-manifest.json"
```

### 12.2 最短 TTL 自动回收

```bash
TTL_KEY="ttl-$ACCEPT_RUN"
curl -fsS -X POST "$BASE_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $TTL_KEY" \
  --data '{"image":"debian:bookworm-slim","ttl_seconds":60}' \
  > "$EVIDENCE_ROOT/ttl-create.json"
export TTL_SANDBOX_ID="$(jq -er '.id' "$EVIDENCE_ROOT/ttl-create.json")"

TTL_TERMINATED=false
for attempt in $(seq 1 180); do
  STATE="$(curl -fsS "$BASE_URL/v1/sandboxes/$TTL_SANDBOX_ID" | jq -r '.state')"
  if [ "$STATE" = Terminated ]; then TTL_TERMINATED=true; break; fi
  sleep 1
done
test "$TTL_TERMINATED" = true
```

### PASS 标准

- 延长成功，相同 expiry no-op，缩短返回 `409`；
- SQLite/API 与 `lease.json` 最终一致；
- 旧 timer 不会删除已续期 sandbox；
- 最短 TTL sandbox 自动进入 `Terminated` 并清理 runtime 资源。

## 13. C08：Race、真实 Docker 与崩溃恢复

### 13.1 Race 检查

```bash
go test -race \
  ./internal/application/... \
  ./internal/reconcile/... \
  ./internal/runner/... \
  ./internal/runtime/... \
  ./internal/store/sqlite/... \
  2>&1 | tee "$EVIDENCE_ROOT/go-test-race.txt"
```

### 13.2 Integration 环境

```bash
export MINISANDBOX_INTEGRATION=1
export MINISANDBOX_TEST_DATA_ROOT="/tmp/minisandbox-it-$ACCEPT_RUN"
export MINISANDBOX_TEST_IMAGE='debian:bookworm-slim'
install -d -m 0700 "$MINISANDBOX_TEST_DATA_ROOT"
```

非默认 Docker endpoint 继续沿用 `MINISANDBOX_TEST_DOCKER_HOST`。

### 13.3 生命周期与可靠性组

```bash
go test -tags=integration -v ./tests/integration/... \
  -run '^(TestDockerHarnessCleanupIsScopedToCurrentLabel|TestCreateSandboxEventuallyRunning|TestRuntimeEnsureRepeatedDoesNotDuplicateResources|TestCreateFailureCompensatesAllManagedResources|TestDeleteSandboxIsIdempotentAndScoped|TestDeleteSandboxRecoversFromExternallyRemovedContainer|TestSandboxdRestartRecoversRunningSandbox|TestCleanupPendingCanResumeAfterBlockerRemoval|TestPeriodicScannerRecoversDroppedCreateAndDeleteWake|TestIdempotencyReplaysCommittedCreateAfterLostResponse|TestCrashHarnessKillsAndRestartsExternalSandboxd|TestCreateCrashPointMatrix|TestDeleteCrashPointMatrix)$' \
  -count=1 -timeout=60m \
  2>&1 | tee "$EVIDENCE_ROOT/integration-lifecycle-reliability.txt"
```

### 13.4 Execution 与安全组

```bash
go test -tags=integration -v ./tests/integration/... \
  -run '^(TestSandboxInitReapsOrphansInContainer|TestExecutionArgvPreservesArgumentBoundaries|TestExecutionStreamsPreserveBytesAndSequence|TestExecutionNonZeroExitRemainsExited|TestExplicitCancelTerminatesProcessTree|TestExecutionTimeoutTerminatesProcessTree|TestForegroundClientDisconnectTerminatesProcessGroup|TestBackgroundClientDisconnectDoesNotCancel|TestPublicBackgroundLogsCursorContract|TestExecutionOutputLimitDrainsProcess|TestExecutionConcurrencyLimitIsAtomic|TestExecutionIdentityHasNoRootOrCapabilities|TestExecutionUserCannotConnectRunnerSocket|TestExecutionEnvironmentAndSecretsAreIsolated|TestExecutionCWDRejectsTraversalAndSymlinks|TestSandboxContainerUsesFixedPhase1SecurityProfile|TestManagedResourceLabelsContainOnlyRecoveryMetadata|TestRuntimeSocketsAreIsolatedPerSandbox|TestDeleteSandboxCleansExecutionsSidecarAndRuntime)$' \
  -count=1 -timeout=45m \
  2>&1 | tee "$EVIDENCE_ROOT/integration-execution-security.txt"
```

### PASS 标准

- race 和两个 integration 组退出码均为 0；
- 上述精确列出的测试没有任何 `SKIP`；
- crash harness 确实强杀并重启外部 `sandboxd`，不是 goroutine 模拟；
- create/delete crash matrix 的每个 crashpoint 最终收敛；
- 无残留进程、container、volume、socket 或 runtime directory。

## 14. C09：稳定 Running 的控制面强杀恢复

本步骤只强杀 C02 启动的专用 PID：

```bash
kill -KILL "$SANDBOXD_PID"
wait "$SANDBOXD_PID" 2>/dev/null || true

./bin/sandboxd -config "$MS_CONFIG" \
  >>"$EVIDENCE_ROOT/sandboxd.log" 2>&1 &
export SANDBOXD_PID=$!

RECOVERED=false
for attempt in $(seq 1 90); do
  if curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1; then
    STATE="$(curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID" | jq -r '.state')"
    if [ "$STATE" = Running ]; then RECOVERED=true; break; fi
  fi
  sleep 1
done
test "$RECOVERED" = true
```

恢复后再次执行一条命令：

```bash
curl -sS -N \
  -X POST "$BASE_URL/v1/sandboxes/$SANDBOX_ID/executions" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data '{"argv":["sh","-c","test -d /workspace && echo recovered"]}' \
  | tee "$EVIDENCE_ROOT/execution-after-restart.sse"
```

### PASS 标准

- 使用相同 SQLite 和 data root 重启；
- readiness 恢复前不提前返回 `200`；
- sandbox 最终仍为 Running；
- 没有创建第二套 main/workspace 资源；
- runner 恢复并能再次执行命令。

## 15. C10：Admin、Metrics、Diagnostics 与日志安全

### 15.1 默认关闭

在当前默认配置下：

```bash
curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/metrics" \
  | tee "$EVIDENCE_ROOT/metrics-disabled.status"
curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/v1/admin/diagnostics" \
  | tee "$EVIDENCE_ROOT/diagnostics-disabled.status"
test "$(cat "$EVIDENCE_ROOT/metrics-disabled.status")" = 404
test "$(cat "$EVIDENCE_ROOT/diagnostics-disabled.status")" = 404
```

### 15.2 启用受保护管理面

```bash
export ADMIN_CONFIG="$MS_ROOT/sandboxd-admin.yaml"
export ADMIN_TOKEN_FILE="$MS_ROOT/admin.token"
export ADMIN_TOKEN="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\r\n')"
printf '%s' "$ADMIN_TOKEN" > "$ADMIN_TOKEN_FILE"
chmod 600 "$ADMIN_TOKEN_FILE"
cp "$MS_CONFIG" "$ADMIN_CONFIG"
sed -i \
  -e 's/^  enabled: false$/  enabled: true/' \
  -e "s#^  token_file: \"\"#  token_file: \"$ADMIN_TOKEN_FILE\"#" \
  "$ADMIN_CONFIG"

kill -TERM "$SANDBOXD_PID"
wait "$SANDBOXD_PID"
./bin/sandboxd -config "$ADMIN_CONFIG" \
  >>"$EVIDENCE_ROOT/sandboxd.log" 2>&1 &
export SANDBOXD_PID=$!
```

等待 `/readyz` 后验证鉴权：

```bash
for attempt in $(seq 1 60); do
  curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1 && break
  sleep 1
done

curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/metrics" \
  | tee "$EVIDENCE_ROOT/metrics-unauthorized.status"
test "$(cat "$EVIDENCE_ROOT/metrics-unauthorized.status")" = 401

curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$BASE_URL/metrics" \
  | tee "$EVIDENCE_ROOT/metrics.txt"
curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$BASE_URL/v1/admin/diagnostics" \
  | tee "$EVIDENCE_ROOT/diagnostics.json"
```

检查高基数和秘密：

```bash
! grep -E 'sandbox_id|execution_id|request_id|token|command|output|environment|container_id|socket|host_path' \
  "$EVIDENCE_ROOT/metrics.txt"
! grep -F "$ADMIN_TOKEN" "$EVIDENCE_ROOT/sandboxd.log"
jq . "$EVIDENCE_ROOT/diagnostics.json" >/dev/null
```

### PASS 标准

- admin disabled 时不注册路由，返回 `404`；
- 启用后 missing、malformed、错误或重复 Authorization 都返回统一 `401`；
- 正确 token 获取 metrics 和 diagnostics；
- metrics label 低基数，scrape 不直接查询 SQLite；
- logs、metrics 和 diagnostics 不泄露 token、命令、输出、环境、raw Docker error 或宿主机路径；
- diagnostics 契约路径漂移解决前，项目最终发布结论仍不能为 PASS。

## 16. C11：Outbound Sidecar 与网络隔离

完整项目核心验收必须提供经过批准的精确 egress 镜像引用：

```bash
export MINISANDBOX_TEST_EGRESS_IMAGE='registry.example/minisandbox-egressd@sha256:<64-hex-digest>'
printf '%s' "$MINISANDBOX_TEST_EGRESS_IMAGE" \
  | grep -Eq '^.+@sha256:[0-9a-f]{64}$'
```

执行三项网络验收：

```bash
go test -tags=integration -v ./tests/integration/... \
  -run '^(TestOutboundSandboxIsDeniedByDefault|TestEgressSidecarTopologyAndLeastPrivilege|TestEgressImmutableCIDRPolicy)$' \
  -count=1 -timeout=30m \
  2>&1 | tee "$EVIDENCE_ROOT/integration-egress.txt"
```

### 清单

- [ ] 服务端默认拒绝 outbound 请求；
- [ ] 显式启用后每个 sandbox 只有一个 egress sidecar；
- [ ] main 与 sidecar 共享预期 network namespace；
- [ ] sidecar 无 Docker socket、host mount、公开 port 和 restart policy；
- [ ] nft policy hash、image digest、protocol 与 netns attestation 一致；
- [ ] sidecar bootstrap 完成后为非 root 且 capability 清零；
- [ ] 内置内部/保留 CIDR、Docker subnet 和 gateway 无条件拒绝；
- [ ] 测试夹具允许的外部目标可以访问；
- [ ] sidecar/gate 异常时新 execution fail closed；
- [ ] sandbox 删除时 main、sidecar、workspace 和 runtime 一起清理。

### PASS 标准

- 三个测试真实运行且退出码为 0；
- `TestEgressSidecarTopologyAndLeastPrivilege` 和 `TestEgressImmutableCIDRPolicy` 不得 `SKIP`；
- 缺少 digest、nft、netns 权限或合适 Docker 环境时记为 `BLOCKED`，不能标记为 PASS。

## 17. C12：删除、资源清理与最终残留审计

删除主验收 sandbox：

```bash
curl -sS -o /dev/null -w '%{http_code}' \
  -X DELETE "$BASE_URL/v1/sandboxes/$SANDBOX_ID" \
  | tee "$EVIDENCE_ROOT/delete-first.status"

TERMINATED=false
for attempt in $(seq 1 120); do
  STATE="$(curl -fsS "$BASE_URL/v1/sandboxes/$SANDBOX_ID" | jq -r '.state')"
  if [ "$STATE" = Terminated ]; then TERMINATED=true; break; fi
  sleep 1
done
test "$TERMINATED" = true
```

重复删除：

```bash
curl -sS -o /dev/null -w '%{http_code}' \
  -X DELETE "$BASE_URL/v1/sandboxes/$SANDBOX_ID" \
  | tee "$EVIDENCE_ROOT/delete-replay.status"
grep -Eq '^(202|204)$' "$EVIDENCE_ROOT/delete-replay.status"
```

精确审计资源：

```bash
docker ps -a --filter "label=minisandbox.io/id=$SANDBOX_ID" \
  --format '{{.ID}} {{.Names}} {{.Labels}}' \
  | tee "$EVIDENCE_ROOT/remaining-containers.txt"
docker volume ls --filter "label=minisandbox.io/id=$SANDBOX_ID" \
  --format '{{.Name}} {{.Labels}}' \
  | tee "$EVIDENCE_ROOT/remaining-volumes.txt"
test ! -e "$MS_RUN_ROOT/$SANDBOX_ID"
test ! -s "$EVIDENCE_ROOT/remaining-containers.txt"
test ! -s "$EVIDENCE_ROOT/remaining-volumes.txt"
```

停止专用控制面：

```bash
kill -TERM "$SANDBOXD_PID"
wait "$SANDBOXD_PID"
trap - EXIT
```

检查 integration harness 残留：

```bash
docker ps -a --filter 'label=io.minisandbox.integration-test-id' \
  --format '{{.ID}} {{.Names}} {{.Labels}}' \
  | tee "$EVIDENCE_ROOT/remaining-integration-containers.txt"
docker volume ls --filter 'label=io.minisandbox.integration-test-id' \
  --format '{{.Name}} {{.Labels}}' \
  | tee "$EVIDENCE_ROOT/remaining-integration-volumes.txt"
```

如果结果不为空，先保留完整证据，再按精确 test ID 分析和清理；不得使用宽泛名称或 prune。

## 18. 最终验收记录模板

| 编号 | 验收项 | 结果 | 主要证据 | 备注 |
|---|---|---|---|---|
| C00 | 全仓测试、vet、契约 |  | `go-test-all.txt`、`go-vet.txt` |  |
| C01 | Linux artifacts |  | `make-build.txt`、`artifact-file-types.txt` |  |
| C02 | 启动、health、readiness |  | `healthz.json`、`readyz.json` |  |
| C03 | 创建、查询、幂等 |  | `create-*.json`、`create-*.headers` |  |
| C04 | 前台 SSE |  | `foreground.sse` |  |
| C05 | 后台、日志、取消 |  | `background-*.json` |  |
| C06 | 身份与安全配置 |  | `execution-identity.sse`、`main-inspect.json` |  |
| C07 | TTL 与 renew |  | `renew-*.json`、`ttl-create.json` |  |
| C08 | Race、Docker、crash matrix |  | `go-test-race.txt`、`integration-*.txt` |  |
| C09 | 控制面强杀恢复 |  | `sandboxd.log`、`execution-after-restart.sse` |  |
| C10 | Admin 与观察面 |  | `metrics.txt`、`diagnostics.json` |  |
| C11 | Outbound 隔离 |  | `integration-egress.txt` | 不得关键 SKIP |
| C12 | 删除与残留审计 |  | `remaining-*.txt` | 必须为空 |
| FINAL | 最终结论 |  | Git SHA 与验收人签名 | PASS / FAIL / BLOCKED |

最终报告还应记录：

- Git SHA 和工作树状态；
- OS、arch、Go 和 Docker 版本；
- Docker endpoint 类型；
- 用户镜像和 egress 镜像的精确引用；
- 所有命令退出码和 `SKIP` 项；
- crashpoint 列表与 race 结果；
- 容器、volume、目录和进程残留；
- 已知契约漂移、未执行项和阻塞原因。

只有所有强制项都有可复核证据，且没有把未运行、`SKIP` 或已知 contract failure 隐藏掉，才能宣布 MiniSandbox 当前 SHA 的核心功能验收通过。
