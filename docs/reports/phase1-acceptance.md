# Phase 1 最终验收报告

## 1. 结论

**验收状态：通过。**

截至 2026-08-01，MiniSandbox Phase 1 已形成可运行的 Docker 生命周期闭环：

```text
POST 202 → Pending/Creating → Running
GET  查询当前状态
DELETE 202 → Stopping → Terminated
重复 DELETE → 204
```

创建失败补偿、删除重试、控制面重启恢复、安全配置、labels 白名单和每 sandbox
独立 Unix Socket 均已在真实 Linux/amd64 Docker Engine 上验证。Phase 1 没有
加入命令执行、网络、TTL 自动回收、Pool、快照或 Kubernetes 能力。

本结论针对以下不可变范围：

- Phase 1 起始基线：`9627f281d7b9618b78c70fcb32a727d633a14bcf`；
- P1-070 完成基线：`acc1a1c60c807ece640c2a7e85ebdf9c6c58eb54`；
- **Phase 1 最终验收基线（P1-078）：
  `adc0c6fbb5d39ea27710df01a080fca4203577d9`。**

P1-079 报告提交位于最终验收基线之后；报告不能在自身内容中记录自身 commit
SHA，因此审计时应以本节记录的验收基线校验代码和文档内容，再从 Git 历史读取
紧随其后的报告提交。

## 2. 依赖与工具版本

### 2.1 直接生产依赖

| 依赖 | 版本 | 用途 |
|---|---:|---|
| `github.com/moby/moby/api` | `v1.55.0` | Docker API 类型 |
| `github.com/moby/moby/client` | `v0.5.0` | Docker Engine client |
| `modernc.org/sqlite` | `v1.54.0` | 纯 Go SQLite driver |
| `go.yaml.in/yaml/v3` | `v3.0.4` | 严格 YAML 配置加载 |
| `github.com/distribution/reference` | `v0.6.0` | 镜像引用解析 |
| `github.com/containerd/errdefs` | `v1.0.0` | Docker 错误分类 |

### 2.2 构建和检查工具

| 工具 | 版本 |
|---|---|
| Go | `go1.26.4 windows/amd64` |
| staticcheck | `2026.1 (v0.7.0)` |
| Docker Client / Engine | `29.3.1 / 29.3.1` |
| Docker API | `1.54` |
| containerd | `v2.2.1` |
| runc | `1.3.4` |

staticcheck 通过
`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...` 执行，避免 `latest`
随时间漂移。

## 3. 测试环境

- 宿主开发环境：Windows amd64；
- Linux 执行环境：Ubuntu WSL2，kernel
  `6.18.33.2-microsoft-standard-WSL2`，`x86_64`；
- Docker daemon：Docker Desktop，storage driver `overlayfs`；
- 测试镜像：
  `debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818`；
- Linux artifact：使用宿主 Go 1.26.4、`GOOS=linux`、`GOARCH=amd64`、
  `CGO_ENABLED=0` 交叉构建，在 WSL 中以真实 ELF 执行；
- Linux integration test 使用 WSL 已缓存的 Go 1.26.0 toolchain 编译和运行；
- Docker Desktop bind source 使用 root 拥有的短临时 data root，代理将其映射到
  cross-distro bind mount；临时数据目录在验收后已删除。

Unix Socket、权限、inode、容器 mount 和 Docker 行为均发生在真实 Linux
环境。安全 profile 测试对原生 Linux 要求 bind source 路径精确相等；仅在
Docker Desktop 返回固定 WSL 重写格式时，才通过 `os.SameFile` 证明代理路径与
预期 runtime directory 是同一文件对象。

## 4. 静态检查与普通回归

| 检查 | 结果 |
|---|---|
| gofmt 全仓库检查 | PASS |
| `go test ./...` | PASS |
| `go test -race ./...` | PASS |
| `go vet ./...` | PASS |
| `go vet -tags=integration ./tests/integration/...` | PASS |
| `staticcheck ./...` | PASS |
| `git diff --check` | PASS |
| linux/amd64 `sandboxd` build | PASS，静态链接 ELF x86-64 |
| linux/amd64 `runnerd` build | PASS，静态链接 ELF x86-64 |
| linux/amd64 `sandbox-init` build | PASS，静态链接 ELF x86-64 |

staticcheck 第一次执行发现六个 ST1005 和一个 Moby SDK SA1019。验收前以独立的
行为不变修复处理：内部错误字符串改为小写开头，并移除 Moby client 已弃用、
且在当前版本为空操作的 `WithAPIVersionNegotiation`；SDK 默认仍启用版本协商。
聚焦测试和最终全量检查随后全部重新执行并通过。

## 5. 真实 Docker integration/security suite

以下测试在同一个最新 Linux integration test binary 中顺序执行，全部 PASS：

| 测试 | 对应证据 |
|---|---|
| `TestRuntimeEnsureRepeatedDoesNotDuplicateResources` | Ensure 幂等，不重复创建资源 |
| `TestDockerHarnessEmptyCleanup` | 空 cleanup 可重复 |
| `TestDockerHarnessCleanupIsScopedToCurrentLabel` | 测试清理不越过 label 边界 |
| `TestCleanupPendingCanResumeAfterBlockerRemoval` | cleanup pending 可在阻塞解除后恢复 |
| `TestDeleteSandboxIsIdempotentAndScoped` | 重复删除幂等且不影响其他 sandbox |
| `TestDeleteSandboxRecoversFromExternallyRemovedContainer` | 外部删除容器后仍能收敛 |
| `TestCreateFailureCompensatesAllManagedResources` | 创建失败不遗留受管资源和目录 |
| `TestSandboxdRestartRecoversRunningSandbox` | 重启后复用 container 并恢复 Store 关联 |
| `TestCreateSandboxEventuallyRunning` | POST 202，最终 Running，runner health 成功 |
| `TestManagedResourceLabelsContainOnlyRecoveryMetadata` | container/volume labels 精确白名单 |
| `TestReviewedManagedLabelsAllowPlatformLabels` | 平台 label 不会扩大 MiniSandbox 恢复协议 |
| `TestSandboxContainerUsesFixedPhase1SecurityProfile` | 权限、网络、caps、mount 和资源限制 |
| `TestRuntimeSocketsAreIsolatedPerSandbox` | socket 路径、inode、0600/0700 和删除隔离 |

失败补偿和 cleanup pending 场景的运行日志只记录稳定 reason：
`ARTIFACT_INJECTION_FAILED`、`CLEANUP_PENDING`，没有输出 daemon cause、配置路径
或请求正文。

套件结束后的只读检查结果：

- `minisandbox.io/managed=true` container：0；
- `minisandbox.io/managed=true` volume：0；
- `io.minisandbox.integration-test-id` container/volume：0；
- integration diagnostic image：0；
- WSL 短数据目录：不存在。

## 6. Phase 1 验收条件映射

| Phase 1 条件 | 自动化或人工证据 | 结果 |
|---|---|---|
| create 立即返回 202，最终 Running | `TestCreateSandboxEventuallyRunning`；手工生命周期流程 | PASS |
| 任一步失败不遗留 container、非持久 volume、runtime dir | `TestCreateFailureCompensatesAllManagedResources` | PASS |
| DELETE 幂等且不删除其他 sandbox | 两个 delete integration tests；socket isolation test | PASS |
| sandboxd 重启恢复 Store 与 Docker 关联 | `TestSandboxdRestartRecoversRunningSandbox` | PASS |
| `/readyz` 等待 Store、Docker、artifact、recovery、worker | bootstrap/readiness 单测；手工启动等待 ready | PASS |
| 每 sandbox 独立 Unix Socket，无 TCP 端口 | socket isolation 与 security profile integration tests | PASS |
| labels 和日志不包含秘密 | labels integration；错误映射/分类单测；失败日志人工检查 | PASS |
| Phase 1 不提供命令执行，runner 明确 501 | `runner.NewServer` 路由人工审查；Phase 1 公共 OpenAPI 无 execution endpoint | PASS |

日志结论由结构化错误单测、实际失败路径日志和 Phase 1 请求面共同支持，但本阶段
没有实现“收集所有进程 stdout/stderr 后做任意秘密字节扫描”的通用测试工具。
这不是 P1-076 的范围，后续增加集中日志组件时必须另建安全测试。

## 7. 手工运维文档验收

[Phase 1 Docker 生命周期指南](../getting-started/phase1-docker-lifecycle.md) 已
覆盖：

- Linux/amd64 构建；
- 安全配置和 loopback 启动；
- `/healthz` 与 `/readyz`；
- POST create、GET 轮询、DELETE、重复 DELETE；
- Docker inspect；
- 正常清理和按精确 sandbox ID 的异常清理；
- Phase 1 能力边界和限制。

在 WSL/Docker Desktop 中使用等价的非默认 Docker socket 实际执行后得到：

```text
create=202
running=yes
delete=202
terminated=yes
repeated_delete=204
cleanup=yes
```

WSL 环境未安装 `jq`，验收脚本使用 Python 标准库完成同一 JSON 断言；文档已经
把 `jq` 列为明确前置工具。除 Docker socket 路径和 JSON 查看工具外，构建、
配置、HTTP 生命周期与清理步骤均按文档执行。

文档没有建议 privileged、host network、向 sandbox 挂载 Docker socket、
发布容器端口或按名称通配符批量删除。

## 8. 已知限制

- 只支持 `linux/amd64`；
- `sandboxd` 只允许 loopback，不是可直接公网发布的多租户服务；
- runner execution、SSE、timeout、cancel、后台任务尚未实现；
- 完整 PID 1 孤儿回收和用户命令非 root 身份切换属于 Phase 2；
- sandbox 固定 `network=none`，不支持受控出站网络；
- 不支持 TTL 自动回收、renew、`Idempotency-Key`、周期 orphan sweep；
- workspace 删除后不可恢复，不支持文件 API、快照或 Pool；
- 不支持 Kubernetes、gVisor、Kata、microVM 或 nested bubblewrap；
- Docker Desktop 的 WSL bind path 需要短 data root 和代理路径身份校验；原生
  Linux 不需要该适配。

## 9. Phase 2/3 遗留项

### Phase 2

- 冻结并实现 argv/shell 执行协议；
- runner bootstrap 后永久降权；
- stdout/stderr SSE、背压和最终事件；
- timeout/cancel 终止完整进程组；
- 前台与后台任务断开语义；
- 在显式服务端许可下增加受控出站网络。

### Phase 3

- TTL、renew revision 和旧 timer 失效；
- `Idempotency-Key` 与持久化创建结果；
- 周期 reconcile、retry backoff 与 restart recovery；
- orphan container/volume/runtime directory 处理；
- cleanup pending 运维诊断、metrics 和审计事件；
- 更系统的日志秘密扫描与故障注入矩阵。

开始 P2-000 前，应再次运行 Phase 1 核心 create/delete/restart smoke，并确认
没有未知受管资源、`CLEANUP_PENDING` 或未解释的失败测试。
