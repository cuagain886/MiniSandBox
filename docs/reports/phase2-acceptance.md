# Phase 2 最终验收报告

## 1. 结论

**验收状态：未通过。Phase 2 不得标记完成。**

本次 P2-097 已执行可用环境中的完整验证矩阵并归档证据，但存在三个独立阻断项：

1. 固定版本 staticcheck 报告 5 个问题；
2. 完整 integration suite 出现一次后台 execution 时间 GC 失败；聚焦重跑通过，说明存在时序不稳定，
   但不能用重跑覆盖完整矩阵的失败结果；
3. 当前 daemon 是 Docker Desktop，不是原生 Linux Docker，egress netns/nft/coding workflow 三项
   必需验收明确 SKIP；正式 secret scanner 也未安装。

P2-097 的边界规定发现问题时不顺手修生产代码，因此本报告只记录事实。修复必须使用独立任务和
独立提交，随后从本文第 5 节完整重跑；在所有必需项 PASS 前不能把本报告改为“通过”。

## 2. 被测基线和环境

- 被测 commit：`34e031ec5d1de4a285a6fecb0aca3123fb102e13`
- 被测提交：`docs(phase2): add execution operations guide`
- 验收日期：2026-08-09（Asia/Shanghai）
- 宿主开发环境：Windows/amd64
- Linux 测试进程：Ubuntu WSL2，Linux/amd64，kernel `6.18.33.2-microsoft-standard-WSL2`
- Go：宿主 `go1.26.4 windows/amd64`；WSL `go1.26.0 linux/amd64`
- Docker Client/Engine：`29.3.1 / 29.3.1`，API `1.54`
- daemon：Docker Desktop `4.67.0`，Linux/amd64，cgroup v2，overlayfs，runc `1.3.4`
- daemon OperatingSystem：`Docker Desktop`
- 普通 integration 镜像：
  `debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818`
- 本次 egress 镜像：
  `minisandbox-egressd@sha256:22fb5aa55221cfeab288de5f7c17a4f39fc5d35bbb02d3e2af119e2e78f37430`
  （验收后已删除本地 tag/image）

生产直接依赖保持为 `containerd/errdefs v1.0.0`、`distribution/reference v0.6.0`、
`moby/api v1.55.0`、`moby/client v0.5.0`、`yaml/v3 v3.0.4` 和
`modernc/sqlite v1.54.0`；Phase 2 没有新增未审批的 runner 生产模块。

验收配置摘要：控制面仅监听随机 loopback 端口；每个测试使用独立短 data root；execution
UID/GID 为 `65532:65532`；默认 network none；outbound 测试使用服务端显式 allow、digest
egress image 和本地 fixture，设计上不访问真实公网。

## 3. 静态检查、普通测试和构建

| 命令 | 结果 | 证据 |
|---|---|---|
| tracked Go files `gofmt -l` | PASS | 无输出 |
| `go test ./...` | PASS | 全部普通 package 通过 |
| `go test -race ./internal/api ./internal/application ./internal/reconcile ./internal/runner ./internal/runnerclient ./internal/runtime/docker` | PASS | 6 个高并发/状态相关包通过 |
| `go vet ./...` | PASS | 无输出 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet -tags=integration ./tests/integration` | PASS | Linux integration 源码通过 vet |
| Linux/amd64 `runnerd` build | PASS | `CGO_ENABLED=0` 交叉构建 |
| Linux/amd64 `sandbox-init` build | PASS | `CGO_ENABLED=0` 交叉构建 |
| Linux/amd64 `egressd` build | PASS | `CGO_ENABLED=0` 交叉构建 |
| `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...` | **FAIL** | 5 个诊断，见下表 |

staticcheck 失败明细：

| 文件 | 规则 | 诊断 |
|---|---|---|
| `internal/egressnft/bootstrap_test.go:129` | U1000 | `framePolicy` 未使用 |
| `internal/runner/netns_other.go:10` | ST1005 | error string 以大写开头 |
| `internal/runtime/docker/egress.go:36` | SA4006 | `created` 的赋值未使用 |
| `internal/runtime/docker/egress.go:189` | ST1005 | error string 以大写开头 |
| `internal/runtime/docker/egress_sidecar.go:99` | ST1005 | error string 以大写开头 |

## 4. Egress 镜像、SBOM 和 secret 检查

`docker buildx build` 使用 `build/egress/Dockerfile`、`linux/amd64`、
`--sbom=true`、`--provenance=mode=max` 成功。结果为 OCI image index digest
`sha256:22fb5aa55221cfeab288de5f7c17a4f39fc5d35bbb02d3e2af119e2e78f37430`；metadata
记录固定的 Go/Debian base digest、Dockerfile frontend 和 BuildKit Syft scanner material。

`docker scout sbom ... --format spdx` 成功生成 4,853 字节 SPDX，索引 3 个 package。构建和 SBOM
检查后已删除 `minisandbox-egressd:p2-097`。源码使用 tracked-file `git grep` 检查 private-key
header、AWS access key、GitHub token 常见格式，未命中；但 `gitleaks`/`trivy`/同等正式 secret
scanner 均未安装，因此 **secret scan 必需项未满足**，不能以正则检查代称正式扫描 PASS。

## 5. Linux Docker integration/security suite

执行命令的等价形式：

```bash
MINISANDBOX_INTEGRATION=1 \
MINISANDBOX_TEST_DATA_ROOT=/tmp/msp2097 \
MINISANDBOX_TEST_IMAGE=debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
MINISANDBOX_TEST_EGRESS_IMAGE=minisandbox-egressd@sha256:22fb5aa55221cfeab288de5f7c17a4f39fc5d35bbb02d3e2af119e2e78f37430 \
go test -tags=integration -count=1 -timeout=30m ./tests/integration
```

完整 suite 在 179.218 秒后 **FAIL**：

```text
TestCompletedExecutionGCRetention/time
completed executions remained queryable: [exec_jUd7kJ5NawOHVym04-K0SQ]
```

同一 commit 聚焦重跑 `TestCompletedExecutionGCRetention` 的 count/time 两个子测试在 15.251 秒
全部 PASS。该结果把问题缩小为可能的 GC deadline/轮询时序不稳定，但不撤销完整矩阵失败；需要
独立诊断任务确定根因并补充稳定回归证据。

以下必需测试被测试代码主动跳过，理由均为
`native Linux Docker is required for host netns inode attestation`：

| 测试 | 结果 | 缺失证据 |
|---|---|---|
| `TestEgressSidecarTopologyAndLeastPrivilege` | **SKIP** | sidecar/runner/daemon 同一宿主内核的 netns inode 交叉证明 |
| `TestEgressImmutableCIDRPolicy` | **SKIP** | 本地公网夹具允许、内部 CIDR/INPUT/FORWARD 实际 nft 命中 |
| `TestCodingAgentLocalGitWorkflow` | **SKIP** | 经 sidecar 的本地 Git clone/build/test 成功与失败流程 |

`tests/security/` 当前只有说明文档，没有独立 Go package；Phase 2 security 场景位于普通单测和
`tests/integration`。因此 `go test ./tests/security/...` 返回 `no packages to test`，不单列为 PASS。

## 6. G1～G7 门禁映射

| 门禁 | 结果 | 本次证据与缺口 |
|---|---|---|
| G1 Phase 1 验收门 | PASS | P2-000 历史基线有效；本次 lifecycle/integration 未报告 Phase 1 回归，最终受管资源清零 |
| G2 执行协议 | PASS | OpenAPI/contract、普通测试、race、argv/shell/SSE/background/cancel/logs 实现证据通过 |
| G3 runner 身份与 socket | PASS | Linux execution/security 测试未报告失败；固定 UID/GID、socket、capability 和 secret 测试已纳入 suite |
| G4 冷迁移与版本拒绝 | PASS | protocol label/health/recovery contract 与测试通过；无旧受管 sandbox 被接管 |
| G5 Outbound 网络 | **FAIL** | 三项原生 Linux egress/coding 验收 SKIP；无法证明完整隔离语义 |
| G6 Linux 进程语义 | **FAIL** | WSL/Docker Desktop 覆盖真实进程、信号、socket 和删除，但缺少 daemon 同内核 netns/nft 证据 |
| G7 依赖策略 | PASS | 无新增 runner 生产依赖；egress 镜像固定 base/digest 并生成 SBOM/provenance |

此外，设计要求在观察到 egress drift 后关闭新 admission、取消当前全部受管 execution 并写入
`EGRESS_UNHEALTHY`。本次只验证了新 execution 的生产 admission 接线；由于原生场景跳过，尚未
获得“已有 execution 被取消”的最终 Docker 证据，该项并入 G5 失败。

## 7. 第 2.3 节阶段条件映射

| 条件 | 结果 | 证据 |
|---|---|---|
| coding agent clone/build/test | **FAIL** | 必需测试因非原生 daemon SKIP |
| timeout 后无残留子孙进程 | PASS | timeout/process-tree integration 未报告失败 |
| 每次 SSE 恰好一个终止事件 | PASS | contract、terminal arbiter、前台 integration 未报告失败 |
| argv/shell 严格二选一 | PASS | contract/domain/handler 测试 |
| 非零退出为 exited | PASS | `TestExecutionNonZeroExitRemainsExited` 纳入 suite，未报告失败 |
| 前台断开取消、后台断开保留 | PASS | disconnect integration 纳入 suite，未报告失败 |
| stdout/stderr 分离、sequence 单调 | PASS | streams/output/logs 测试纳入 suite，未报告失败 |
| 输出上限后排空并标记截断 | PASS | output-limit integration 纳入 suite，未报告失败 |
| execution 不继承内部 secret | PASS | secret-isolation integration 纳入 suite，未报告失败 |
| cwd 不经 `..`/symlink 逃逸 | PASS | cwd security integration 纳入 suite，未报告失败 |
| 普通 UID 非 root、capability 为零 | PASS | identity/security integration 纳入 suite，未报告失败 |
| 普通命令不能连接 runner socket | PASS | socket security integration 纳入 suite，未报告失败 |
| outbound 拒绝内部 CIDR、允许本地公网夹具 | **FAIL** | 原生 nft 场景 SKIP |
| 删除清理 execution/main/sidecar/runtime | PARTIAL | 删除测试未报告失败，但验收前发现一个历史受管 volume 残留；已精确清理 |

## 8. 资源清理结果

完整 suite 结束后首次清点发现一个无容器引用的历史受管 volume：
`minisandbox-workspace-e325591a-f157-4d3f-83ec-614e69505bdf`，创建时间早于本轮 suite；同时存在
空的服务级 `minisandbox-egress` network。经 `docker volume/network inspect` 确认无容器引用后，
已按精确全名删除。P2-093 与本轮产生的两个 egress 测试镜像也已按精确 tag 删除，均不可恢复、
但可由固定 Dockerfile 重建。

最终只读清点：

- `minisandbox.io/managed=true` container：0
- `minisandbox.io/managed=true` volume：0
- `io.minisandbox.integration-test-id` container：0
- `io.minisandbox.integration-test-id` volume：0
- `minisandbox.io/managed=true` network：0
- `minisandbox-egress*` 本地测试 image：0
- WSL `/tmp/msp2097` 子目录和根目录：0/已删除

## 9. 后续重验入口

必须先用独立任务处理 staticcheck、GC 时序不稳定和正式 secret scanner。随后在原生 Linux/amd64
Docker Engine 上配置固定 digest 的 Debian、egress、Go+Git agent 镜像，执行第 3～5 节全部命令，
确保没有 SKIP/FAIL，并重新清点资源。只有 G1～G7 与第 2.3 节全部 PASS，才能提交替代本报告结论
的新版验收证据。
