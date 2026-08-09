# ADR-0002：固定 Phase 2 egress sidecar 与 artifact contract

- 状态：已接受（2026-08-09，依据 G5、G7 与 P2-064）
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
6. bootstrap 只通过 Docker attach/start 的一次性长度前缀 stdin 传入，不进入
   environment、command、labels、主 sandbox mount 或 host bind mount；
7. nft 安装与回验完成后写出有界 attestation，永久丢弃 `NET_ADMIN`，随后仅作为
   network namespace anchor 存活；
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
| capability | create 时 `CapDrop=ALL, CapAdd=NET_ADMIN`；Ready 时 `CapEff/CapPrm/CapAmb` 均不含 `NET_ADMIN` |
| restart | `no` |
| filesystem | read-only rootfs；只允许受管、有限大小的 attestation tmpfs |
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

## 4. 一次性 bootstrap framing

stdin 恰好包含一帧：

```text
4-byte uint32 big-endian JSON length
1..65536 bytes strict UTF-8 JSON
EOF
```

JSON schema 只允许：protocol version、rule schema version、policy hash、规范化 IPv4
deny set、规范化 IPv6 deny set、受管 network identity 以及固定 anchor identity。
字段由 sandboxd 从可信配置、Docker inspect 和 immutable policy 重建，公共
sandbox/execution 请求不能提供或覆盖。

以下情况全部非零退出且不得写 Ready attestation：空帧、超限、提前 EOF、尾随
字节、第二帧、未知字段、重复字段、非法 UTF-8、未知版本、policy hash 不匹配、
network identity 不匹配和 stdin 中断。读取完成后立即关闭输入并清零暂存 buffer。

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

## 6. Readiness attestation

只有规则安装和回验成功后才能原子写入最大 4096 bytes 的 UTF-8 JSON regular
file。schema 封闭且只含：

- egress protocol version；
- rule schema version；
- policy hash；
- `linux-netns:<device>:<inode>`；
- artifact image digest；
- UTC 生成时间。

attestation 不含完整 CIDR、凭据、container ID、host path 或错误文本。sandboxd
必须把 attestation、Docker network mode、sidecar/main sandbox netns identity 和
期望 policy hash 一起验证；单独存在文件不代表 Ready。

## 7. 永久降权

Ready 之前按不可逆顺序执行：关闭 bootstrap stdin、拒绝主 GID 之外的任何
supplementary group、清除 ambient/permitted/effective capabilities、回验固定
非 root identity、设置 `no_new_privs` 并读取 `/proc/self/status`。Docker/runc
重复注入的主 GID 不扩大组权限，可以保留；出现任意其他 GID 或非零 capability
都立即退出。不得为了清空这个等价重复项而增加 `CAP_SETGID`。

Ready 后 anchor 不再拥有安装、修改或删除 nft 规则的能力，不接受新配置，也不
fork/exec `nft`。主 sandbox 共享 netns 但不共享 sidecar filesystem、PID namespace
或 capability，且自身仍为 `CapDrop=ALL`。

## 8. 供应链与发布门禁

每个候选 digest 必须同时具备：

1. 可复现或可验证的构建 provenance；
2. SPDX SBOM 与许可证清单；
3. vulnerability scan 结果及明确的风险接受记录；
4. `linux/amd64` 平台证明；
5. `egressd`、bootstrap framing、nft transaction、attestation 和 capability drop
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

- bootstrap framing 的长度、EOF、尾随、重放、未知字段和版本矩阵；
- IPv4/IPv6 deny、内部 subnet/gateway、INPUT 主动连接拒绝、FORWARD 全拒绝；
- nft 不存在、transaction 失败、回验漂移和 policy hash 不符；
- `CapEff/CapPrm/CapAmb` 清零、无监听 socket、无 Docker socket/host mount/device；
- anchor 信号退出、sidecar unhealthy、execution admission 关闭和完整重建；
- image digest、SBOM、provenance、protocol/schema 和 attestation 的一致性；
- 日志、错误、labels、inspect 和公共 API 中不存在完整 policy 或 bootstrap secret。

## 11. 明确不做

Phase 2 不提供域名 allowlist、HTTP proxy、动态规则更新、每次 execution 网络策略、
平台公网资产强制枚举、Kubernetes CNI、热升级或 sidecar 管理 API。任何此类能力都
会改变隔离语义，必须先更新威胁模型并新建设计门禁。
