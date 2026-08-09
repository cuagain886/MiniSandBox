# ADR-0002：固定 Phase 2 egress sidecar 与 artifact contract

- 状态：已接受（2026-08-09，依据 G5、G7 与 P2-064；同日补充可重连控制协议）
- 决策日期：2026-08-09
- 对应任务：P2-064
- 适用范围：Phase 2 outbound sandbox 的 egress namespace anchor

## 1. 背景

Phase 2 的 outbound 会改变 `network=none` 的隔离语义。采用的边界是“每个
sandbox 一个 egress sidecar 持有网络命名空间，主 sandbox 共享该命名空间，
sidecar 通过 nftables 无条件屏蔽内部 CIDR”。egress sidecar 因而是独立安全
主体，而不是普通辅助进程。

本 ADR 在生产实现前冻结 artifact、bootstrap、readiness、权限、升级和回滚
契约。详细任务顺序见
[Phase 2 开发计划](../phase-2-runner-execution-development-plan.md)，总体隔离边界见
[全 Go runtime 设计](../all-go-agent-sandbox-runtime-design.md)。

## 2. 决策摘要

1. sidecar 是 MiniSandbox 自行维护的最小 OCI image，只包含第一方 Go
   `egressd`、固定 `nft` userspace 及其运行时库和证书/时区之外的必要基础文件；
2. 不引入或裁剪完整 OpenSandbox egress stack，不运行代理、DNS server、管理
   API、shell supervisor 或包管理器；
3. image 只接受 `name@sha256:<64 lowercase hex>`，配置和受管资源必须保存同一
   digest；tag-only、可变 tag、未知 digest 和隐式 `latest` 全部 fail closed；
4. Phase 2 只发布 `linux/amd64`，不做运行时架构模拟；
5. egress bootstrap protocol version 固定为 `1`，规则 schema version 固定为
   `1`；未知、旧版或未来版本均不得 Ready；
6. sandboxd 只通过可重连 Docker attach stdin/stdout 与 sidecar 交互；首个请求是
   唯一 bootstrap，后续请求只能 inspect，每次都有新的 request ID 与随机 nonce；
7. nft 安装与回验完成后，egressd 永久丢弃 `NET_ADMIN`，把 attestation 仅保存在
   进程内存，并在每次 inspect 时重新回验身份、capability 与 netns 后返回；
8. `RestartPolicy=no`。anchor 退出后关闭新 execution admission、取消已有受管
   execution，并通过删除和完整重建恢复，不做无配置自动重启。

## 3. Artifact contract

| 项目 | 固定要求 |
|---|---|
| 所有者 | MiniSandbox release engineering；代码审查归 runtime/security owners |
| image reference | 配置显式提供的 OCI digest reference；仓库不提供 tag-only 默认值 |
| source | 本仓库版本化 `egressd` 源码、锁定的 Go toolchain 和锁定 nft runtime build input |
| 平台 | `linux/amd64` |
| egress protocol | 整数 `1` |
| rule schema | 整数 `1` |
| entrypoint | 固定第一方 `egressd bootstrap`，普通请求不能覆盖 |
| user | bootstrap 阶段 root；Ready 前永久降为固定非 root anchor UID/GID |
| capability | create 时 `CapDrop=ALL, CapAdd=NET_ADMIN,SETUID,SETGID`；Ready 时 `CapEff/CapPrm/CapAmb` 全部为零 |
| restart | `no` |
| filesystem | read-only rootfs；不挂载 tmpfs、bind mount 或 volume |
| control | `OpenStdin=true`、`StdinOnce=false` 的 Docker attach stdin/stdout；每次 exchange 有界且可取消 |
| logs | Docker `log-driver=none`；控制响应不得进入 Docker logs |
| network/listen | 不发布端口，不监听 TCP、HTTP 或 Unix Socket，不提供策略管理 endpoint |

镜像 digest 是完整供应链版本。具体 `egressd` commit、Go toolchain、`nft --version`、
基础 rootfs、许可证和每个 OS package 的精确版本必须写入该 digest 对应的 SPDX
SBOM 和 provenance；缺少任一产物的 digest 不得进入生产配置。这样避免在配置
模型中重复维护可能与真实 image 漂移的包版本，同时仍由 digest 精确固定实际
二进制集合。

release manifest 必须建立以下不可变映射：

```text
image digest -> source commit -> egress protocol -> rule schema
             -> Go toolchain -> nft version -> SPDX SBOM digest
             -> provenance digest -> linux/amd64
```

P2-064 不选择一个公共 registry 或虚构 digest。部署者可以镜像到私有 registry，
但内容 digest、SBOM 和 provenance 必须保持一致；registry hostname 不是信任根。

## 4. 可重连 attach 控制协议

egressd 的 stdin/stdout 是唯一控制通道。sandboxd 每次操作重新 Docker attach，写入
一个请求帧并读取一个响应帧，然后关闭本次 attach；关闭连接不关闭容器 stdin，
egressd 继续在同一进程中等待下一帧。每个帧都是：

```text
4-byte uint32 big-endian JSON length
1..上限 bytes strict UTF-8 JSON
```

请求 envelope 字段封闭，包含 `protocol_version`、`type`、`request_id`、`nonce`，
以及仅 bootstrap 请求允许出现的 `bootstrap`。`request_id` 是每次新生成的 128-bit
小写十六进制值，`nonce` 是每次新生成的 256-bit 随机挑战；响应必须原样回显二者，
否则 sandboxd 拒绝结果。外层请求上限为 bootstrap 最大 65536 bytes 加固定 envelope
余量，响应上限为 4096 bytes；读取不依赖 EOF，也不接受一帧中的尾随 JSON 值。

状态机封闭为：首个请求必须是 `bootstrap`，成功后只能是 `inspect`。bootstrap
payload 只允许 protocol/rule schema、policy hash、规范化 IPv4/IPv6 deny sets、
预期 netns、image digest 与固定 anchor UID/GID；字段由 sandboxd 从可信配置、Docker
inspect 和 immutable policy 重建，公共 sandbox/execution 请求不能提供或覆盖。
inspect 不含策略 payload，也不能更新任何状态。

空帧、超限、提前 EOF、未知或重复字段、尾随 JSON、未知版本/类型、无效关联字段、
inspect-before-bootstrap、重复 bootstrap、policy hash/netns 不匹配以及读写中断都会
使 egressd 非零退出并关闭该 network namespace。sandboxd 对 attach、读写和响应
校验使用同一有界 deadline/context，并按 sandbox 串行化控制 exchange；不会把未知
状态当作 Ready。

## 5. nft 职责与规则边界

image 内只允许固定路径的 `nft`，`egressd` 使用参数化 stdin 调用 `nft -f -`，禁止
shell、PATH 搜索和用户字符串拼接。实际 `nft` 版本由 image digest 与 SBOM 固定，
release validation 必须记录 `nft --version`；升级 nft 必须产生新 digest 并执行本
ADR 第 10 节的完整回归，不能原地替换。

规则只创建独立 `inet minisandbox_egress` table，并按 schema v1 固定：

- OUTPUT：loopback、established/related、immutable IPv4/IPv6 deny set、允许其余
  公网、默认拒绝异常状态；
- INPUT：只允许 loopback、established/related 和 schema 固定的必要 ICMP/ICMPv6；
- FORWARD：无条件拒绝；
- deny set 始终包含内置安全基线和实际受管 Docker subnet/gateway，运维配置只能
  追加，不能删除基线。

安装必须是单次 transaction。安装后回读 table、chain、hook、priority、policy、
set 内容和 policy hash；任何缺失或额外允许规则均 fail closed。失败路径不得清空
规则后继续 Ready，也不得退化为未过滤公网网络。

## 6. 进程内 attestation

只有规则安装、netns 校验与永久降权全部成功后，egressd 才在当前进程内构造
attestation。它不写入文件、tmpfs、Docker logs、environment、labels 或命令行；
容器退出即丢失，不能跨重启恢复。schema 封闭且只含：

- egress protocol version；
- rule schema version；
- policy hash；
- `linux-netns:<device>:<inode>`；
- artifact image digest；
- UTC 生成时间。

attestation 不含完整 CIDR、凭据、container ID、host path 或错误文本。每次 inspect
返回前，egressd 都重新回读当前 netns、UID/GID、supplementary groups、
`CapEff/CapPrm/CapAmb` 与 `NoNewPrivs`；任一漂移直接退出而不返回旧证明。sandboxd
还必须验证响应的 request ID/nonce、Docker network mode、sidecar/main sandbox netns
identity、期望 policy hash 与 image digest。单独收到一个结构合法的 JSON 不代表
Ready。

## 7. 永久降权

sidecar 以 root 启动，但从创建时即 `CapDrop=ALL`，只保留安装规则和完成不可逆
身份切换所需的 `NET_ADMIN`、`SETGID`、`SETUID`；它不是 privileged 容器，也没有
shell、host mount、Docker socket、device 或监听端口。首个 bootstrap 请求后先安装
并回验 nft，再执行 `setgroups(nil)`、`setresgid`、`setresuid`，
清除 ambient/permitted/effective capabilities、设置 `no_new_privs` 并读取
`/proc/self/status` 回验固定非 root identity。出现任意额外 GID、非零 capability
或 `NoNewPrivs!=1` 都立即退出，且不得发布 attestation。

Ready 后 anchor 不再拥有安装、修改或删除 nft 规则的能力，只接受无 payload 的
只读 inspect，不 fork/exec `nft`。主 sandbox 共享 netns 但不共享 sidecar filesystem、
PID namespace 或 capability，且自身仍为 `CapDrop=ALL`。

## 8. 供应链与发布门禁

每个候选 digest 必须同时具备：

1. 可复现或可验证的构建 provenance；
2. SPDX SBOM 与许可证清单；
3. vulnerability scan 结果及明确的风险接受记录；
4. `linux/amd64` 平台证明；
5. `egressd`、可重连 attach framing/state machine、nft transaction、进程内
   attestation 和 capability drop
   测试；
6. 使用该精确 digest 的真实 Linux/Docker outbound 隔离验收。

仓库核心 Go module 不因 sidecar 引入生产依赖。若 `egressd` 需要标准库之外的
Go module、动态策略服务或额外守护进程，必须另建 ADR 并重新确认 G7。

## 9. 兼容性、升级与回滚

- protocol/schema 兼容只允许精确相等，不自动接受旧版或未来版；
- artifact 升级先生成新 digest、SBOM、provenance 和 release manifest，再通过
  staging 完整验收；
- 已运行 sandbox 不热切换 image、network namespace 或规则；升级采用停止创建、
  删除旧 sandbox、确认资源清零、部署新配置、重新创建的冷迁移；
- 回滚同样切回上一个已批准 digest 并完整重建，不复用新版本 sidecar、attestation
  或 netns；
- 未知 digest、manifest 缺失、拉取失败、版本不匹配或 attestation 不匹配均保持
  sandbox 非 Running，并进入可解释的补偿/清理流程。

## 10. 必须验证的场景

- attach framing 的长度、提前 EOF、尾随 JSON、关联字段、重连、重复 bootstrap、
  inspect-before-bootstrap、未知字段和版本矩阵；
- IPv4/IPv6 deny、内部 subnet/gateway、INPUT 主动连接拒绝、FORWARD 全拒绝；
- nft 不存在、transaction 失败、回验漂移和 policy hash 不符；
- `CapEff/CapPrm/CapAmb` 清零、无监听 socket、无 Docker socket/host mount/device；
- anchor 信号退出、sidecar unhealthy、execution admission 关闭和完整重建；
- image digest、SBOM、provenance、protocol/schema 和 attestation 的一致性；
- 无 tmpfs/volume/bind mount、`log-driver=none`，Docker logs、错误、labels、inspect
  和公共 API 中不存在完整 policy、attestation 或 bootstrap secret。

## 11. 明确不做

Phase 2 不提供域名 allowlist、HTTP proxy、动态规则更新、每次 execution 网络策略、
平台公网资产强制枚举、Kubernetes CNI、热升级或 sidecar 管理 API。任何此类能力都
会改变隔离语义，必须先更新威胁模型并新建设计门禁。
