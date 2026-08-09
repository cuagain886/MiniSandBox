# Phase 2 最终验收报告

## 1. 结论

**验收状态：通过。Phase 2 核心条件 13/13 PASS，G1～G7 全部 PASS，完整原生
Linux/Docker integration suite 无 FAIL、无 SKIP。**

本轮已经解决旧报告中的 staticcheck、completed execution GC、原生 netns/nft、
coding workflow、正式 secret scan 和 mount-source 校验问题。新增的 egress 控制面采用：

- 每个 sandbox 一个 egress sidecar 持有 network namespace；
- nft 无条件屏蔽 immutable IPv4/IPv6 内部 CIDR；
- 可重连 Docker attach stdin/stdout 请求/响应；
- 首次唯一 bootstrap，之后只读 inspect；
- 每次新的 128-bit request ID 和 256-bit 随机 nonce；
- attestation 只驻留 egressd 进程内存；
- 无 tmpfs、控制文件、Docker logs、exec、mount 或管理 socket 回退；
- 任一协议、身份、capability、policy 或 netns 不确定状态 fail closed。

被测生产代码 SHA 为
`440c73c0fac180d9cf6cb4feec221c75cc880387`。其后的提交只同步设计和本报告，
不改变被测二进制或测试结果。

## 2. 被测环境

| 项目 | 值 |
|---|---|
| 验收日期 | 2026-08-09（Asia/Shanghai） |
| Windows Go | `go1.26.4 windows/amd64` |
| Linux | Ubuntu 24.04.2 LTS，WSL2 kernel `6.18.33.2-microsoft-standard-WSL2` |
| Linux Go | `go1.26.0 linux/amd64` |
| Docker | 原生 Ubuntu Docker Engine `29.1.3`，API `1.52` |
| Docker socket | `unix:///run/minisandbox-native-docker.sock` |
| storage / cgroup / runtime | overlayfs / cgroup v2 systemd / runc |
| 普通测试镜像 | `debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818` |
| egress 测试镜像 | `minisandbox-egress-native@sha256:2080476bdf728fd6fa4c622d1d37ad297147d6586e16842c8cc9b735ea36a7da` |
| coding-agent 测试镜像 | `minisandbox-agent-native@sha256:f5a9133828f2fb77f587f3b8d582a4c2cf6151406b427d11f18ce62c84bad7a6` |

验收使用与 Docker Desktop 分离的原生 dockerd。测试进程和 dockerd 共享同一 WSL2
宿主内核，因此可以交叉验证真实 netns inode、nft、UID/GID、Unix Socket 与
capability；没有用 Docker Desktop 的路径代理或 fake 结果代替 G5/G6 证据。

## 3. 静态检查、普通测试和构建

| 命令 | 结果 | 证据 |
|---|---|---|
| tracked Go files `gofmt -l` | PASS | 无输出 |
| `go test ./...` | PASS | 全部普通 package 通过 |
| `go test -race ./internal/api ./internal/application ./internal/reconcile ./internal/runner ./internal/runnerclient ./internal/runtime/docker` | PASS | 6 个并发/状态相关包通过 |
| `go vet ./...` | PASS | 无输出 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet -tags=integration ./tests/integration` | PASS | integration 源码通过 Linux vet |
| `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...` | PASS | 无输出 |
| Linux/amd64 `runnerd` build | PASS | `CGO_ENABLED=0` |
| Linux/amd64 `sandbox-init` build | PASS | `CGO_ENABLED=0` |
| Linux/amd64 `egressd` build | PASS | `CGO_ENABLED=0` |

旧报告的 5 个 staticcheck 诊断均已消除。runner 终态计算还修复了宿主时钟回拨时把
合法非零退出误写为 `INTERNAL_ERROR` 的问题：持续时间保留 monotonic 语义并对负值
钳制为零，退出状态仍保持 `exited` 和原始 exit code。

## 4. Egress artifact 与供应链证据

最终保留的本地验收镜像为 `minisandbox-egress-native:phase2-final`：

| 产物 | 结果 |
|---|---|
| image/config digest | `sha256:2080476bdf728fd6fa4c622d1d37ad297147d6586e16842c8cc9b735ea36a7da` |
| OCI manifest | `sha256:03653b944fb90c37cccf598cc715d8f1eb3ff1c44c6689bd945b502d4812261f` |
| provenance/SBOM attestation manifest | `sha256:7989d4566cb240d90f92519b0db6d99a679c7ecb99fd55d00b6c41ae17bccaa2` |
| source revision label | `440c73c0fac180d9cf6cb4feec221c75cc880387` |
| inspect size | 3,212,831 bytes |
| standalone SPDX JSON | 2 packages；1,729 bytes；SHA-256 `50ec9e169b31f6a3285fc7d875709533fe4dd184e83213ea99ccfa05cdfa4cfb` |
| build metadata JSON | 2,304 bytes；SHA-256 `c241a6f4690ca74f187fc0a80edc4de099f3c9dca88a4dcb5304c3a2e80b07d3` |
| secret scan | Gitleaks `v8.27.1`，Git history scan PASS |
| vulnerability scan | Grype `v0.116.1`，0 Critical、0 High、无任何 match |

Grype Linux/amd64 发布包按 SHA-256
`0122df7b655981abe547ad3d2190d65551dac6a2bfc80b4dc2a989b5d0587458`
校验后，对 `docker save` 产生的离线镜像归档扫描；扫描容器或进程没有获得 Docker
socket。Gitleaks 使用：

```text
go run github.com/zricethezav/gitleaks/v8@v8.27.1 git \
  --redact --no-banner --no-color --log-level warn .
```

构建和扫描临时文件已删除；OCI 镜像及内置 attestation 保留，便于复核精确 digest。

## 5. 原生 Linux/Docker integration 与 security suite

最终完整执行的等价命令为：

```bash
HOME=/home/xf \
GOMODCACHE=/home/xf/go/pkg/mod \
GOCACHE=/tmp/minisandbox-root-go-cache \
GOFLAGS=-buildvcs=false \
MINISANDBOX_INTEGRATION=1 \
MINISANDBOX_TEST_DOCKER_HOST=unix:///run/minisandbox-native-docker.sock \
MINISANDBOX_TEST_DATA_ROOT=/tmp/msp2final \
MINISANDBOX_TEST_IMAGE=debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
MINISANDBOX_TEST_EGRESS_IMAGE=minisandbox-egress-native@sha256:2080476bdf728fd6fa4c622d1d37ad297147d6586e16842c8cc9b735ea36a7da \
MINISANDBOX_TEST_AGENT_IMAGE=minisandbox-agent-native@sha256:f5a9133828f2fb77f587f3b8d582a4c2cf6151406b427d11f18ce62c84bad7a6 \
go test -tags=integration -count=1 -timeout=30m ./tests/integration
```

结果：**PASS，119.7 秒，39 个顶层 integration/security test 全部执行，无 SKIP。**

关键原生场景：

- `TestEgressSidecarTopologyAndLeastPrivilege`：PASS，验证 main/sidecar 拓扑、共享
  netns、非 root anchor、capability 清零、无 mount/socket/port 和 attach inspect；
- `TestEgressImmutableCIDRPolicy`：PASS，验证 IPv4/IPv6 deny、Docker gateway、INPUT、
  FORWARD、本地“公网目标”放行和 unhealthy fail-closed；
- `TestCodingAgentLocalGitWorkflow`：PASS，47.48 秒，完成经 sidecar 的本地
  clone/build/test 成功与失败流程；
- `TestSandboxContainerUsesFixedPhase1SecurityProfile`：PASS，mount source 必须与预期
  runtime 目录是同一文件身份，且没有 Docker socket 或额外 mount；
- `TestCompletedExecutionGCRetention`：PASS，count/time 两条 retention 路径稳定；
- `TestExecutionNonZeroExitRemainsExited`：PASS，退出 1/2/127 均保留 `exited`；
- cancel、timeout、前台断开、后台断开、输出预算、日志 cursor、并发上限、cwd、
  secret、socket、PID 1、recovery、删除补偿与幂等清理场景全部 PASS。

coding workflow 和 egress CIDR fixture 都只使用隔离的本地服务，不访问真实公网。

## 6. G1～G7 门禁映射

| 门禁 | 结果 | 证据 |
|---|---|---|
| G1 Phase 1 验收门 | PASS | Phase 1 生命周期、安全 profile、mount-source、恢复与删除回归通过；旧受管资源清零 |
| G2 执行协议 | PASS | OpenAPI/protocol/SDK/handler、SSE、argv/shell、后台 status/logs/cancel 全部通过 |
| G3 runner 身份与 socket | PASS | 固定非 root UID/GID、capability 清零、`0600` socket、token/env 隔离取得真实 Linux 证据 |
| G4 冷迁移与版本拒绝 | PASS | 未接管旧资源；未知协议/image/labels/drift fail closed；恢复和清理测试通过 |
| G5 Outbound 网络 | PASS | 每 sandbox sidecar、共享 netns、immutable nft、进程内 attestation、unhealthy admission/cancel 全部通过 |
| G6 Linux 进程语义 | PASS | 原生 dockerd 同内核验证 PID 1、进程组、signal、UID/GID、socket、netns、nft 与 capability |
| G7 依赖与供应链 | PASS | runner 无新增生产依赖；egress digest、SBOM、provenance、Gitleaks 和 Grype 证据齐全 |

## 7. Phase 2 核心条件：13/13 PASS

| # | 条件 | 结果 | 证据 |
|---:|---|---|---|
| 1 | coding agent clone/build/test | PASS | 原生 local Git workflow 成功和失败路径 |
| 2 | timeout 后无残留子孙进程 | PASS | timeout/process-tree integration |
| 3 | 每次 SSE 恰好一个终止事件 | PASS | terminal arbiter、contract 与 integration |
| 4 | argv/shell 严格二选一 | PASS | contract、domain、handler 和 argv/shell E2E |
| 5 | 非零退出保持 `exited` | PASS | 1/2/127 与宿主时钟回拨回归 |
| 6 | 前台断开取消、后台断开保留 | PASS | 两条 disconnect E2E |
| 7 | stdout/stderr 分离、sequence 单调 | PASS | byte-preserving streams 与 logs cursor |
| 8 | 输出超限后持续排空并标记截断 | PASS | output budget E2E |
| 9 | execution 不继承内部 secret | PASS | env/API/logs/inspect 隔离与 Gitleaks |
| 10 | cwd 不经 `..`/symlink 逃逸 | PASS | cwd security E2E |
| 11 | 普通 UID 非 root、capability 为零 | PASS | 原生 identity/capability E2E |
| 12 | 普通命令不能连接 runner socket | PASS | Unix Socket 权限 E2E |
| 13 | outbound 拒绝内部 CIDR、允许本地公网 fixture | PASS | topology、nft、netns 与 coding workflow E2E |

删除 sandbox 的 execution/main/sidecar/runtime 清理是独立的阶段前提，也已 PASS，
不计入上述 13 项分母。

## 8. 资源清理审计

验收后按精确 label、sandbox ID 和测试 data root 清理：

- 原生 dockerd 中 managed/integration 容器：0；
- managed/integration volume：0；
- `minisandbox-egress` 网络在确认 `Containers={}` 后删除；
- `/tmp/msp2final`、`/tmp/msp2att`、root Go cache、测试二进制、SBOM、metadata 和 Grype 临时目录：已删除；
- candidate/diagnostic egress tags 与临时 agent tag：已删除；
- 只保留 `minisandbox-egress-native:phase2-final` 精确验收镜像。

上述临时容器、volume、network、测试文件和候选 tag 已永久删除，不能从 Docker
受管资源中直接恢复；最终 digest 镜像仍保留。

## 9. 验收判断

Phase 2 已达到当前仓库定义的开发完成标准，可以进入后续阶段。该结论不把 Docker
container 描述为 microVM 级恶意多租户隔离，也不扩展 Phase 2 的既定边界：没有
用户自定义 FQDN/CIDR/端口策略、动态规则、代理、热迁移或 sidecar 自动重启；这些
能力必须另行更新威胁模型和设计门禁。
