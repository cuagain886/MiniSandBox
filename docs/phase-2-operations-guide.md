# Phase 2 execution 使用与运维指南

本文只描述 MiniSandbox Phase 2 已实现的能力：在 Linux Docker sandbox 中以固定非 root
身份执行 `argv` 或 `shell` 命令，读取前台 SSE、管理后台 execution，以及按服务端策略选择
默认断网或受控 outbound。公共契约以 [Lifecycle OpenAPI](../api/lifecycle.openapi.yaml)
为准，完整配置参考 [示例配置](../configs/sandboxd.example.yaml)。

本指南使用的公共资源路径为 `/v1/sandboxes`、
`/v1/sandboxes/{sandbox_id}/executions`、
`/v1/sandboxes/{sandbox_id}/executions/{execution_id}` 和
`/v1/sandboxes/{sandbox_id}/executions/{execution_id}/logs`。

## 1. 先理解安全边界

MiniSandbox Phase 2 依赖 Docker 容器边界，不是面向互不信任恶意租户的生产级强隔离平台。
它没有 gVisor、Kata 或 microVM 边界，也没有公共 API 的租户鉴权、配额和审计系统。
`sandboxd` 默认只监听 `127.0.0.1:8080`；不要直接暴露到不受信网络。

- `runnerd` 和用户命令使用同一个固定非 root UID/GID。用户进程不能连接受宿主机目录权限保护的
  runner socket，但同 UID 进程仍可能互相发信号、占用并发槽或制造资源压力。这是已知的
  sandbox 内自我 DoS，不构成跨 sandbox 权限。
- timeout、cancel、前台连接断开和 sandbox 删除都会终止完整进程组，但不能把资源限制误解为
  恶意代码的绝对隔离保证。
- outbound 只屏蔽内置内部/保留 CIDR、实际 Docker bridge subnet/gateway 和运维追加 CIDR。
  它不是 FQDN、域名、端口或应用协议 allowlist。位于公网地址上的云平台敏感服务不会被内置
  CIDR 自动识别；需要运维追加其稳定 CIDR，或在外部代理/防火墙实施更强策略。
- 公共请求只能选择 `network.outbound=true/false`，不能提交 CIDR、network name、sidecar
  image、端口或 capability。

## 2. 环境和构建要求

生产运行面要求 Linux/amd64、Docker Engine、cgroup/resource limit 支持和可用的 Unix Socket。
egress sidecar 镜像内必须包含兼容的 `nftables`；宿主机无需给 sandbox 安装 `nft`，但 Docker
daemon 所在 Linux 内核必须支持 network namespace、conntrack 与 nftables。Windows 仅用于编译
和普通单测，Docker Desktop 不能替代原生 Linux netns 验收。

```bash
make build
make egress-image EGRESS_IMAGE=minisandbox-egressd:phase2
```

`make build` 先生成静态 Linux `runnerd`、`sandbox-init`，再将它们嵌入 `sandboxd`。egress
镜像构建会生成 SBOM、provenance 和 `dist/egress-build-metadata.json`。部署时必须使用经过审批、
可验证 SBOM/provenance 且精确固定为 `repository@sha256:<64 lowercase hex>` 的镜像引用；不能使用
浮动 tag，也不能只记录本地 image ID。

## 3. 主密钥与最小启动配置

runner 主密钥必须是绝对路径下恰好 32 字节、非全零、非 symlink 的 regular file，权限不得宽于
`0600`。不要把密钥写进 YAML、环境变量、命令行、日志或 Git。

```bash
sudo install -d -m 0700 /etc/minisandbox
sudo sh -c 'umask 077; head -c 32 /dev/urandom > /etc/minisandbox/runner-master-key'
sudo test "$(wc -c < /etc/minisandbox/runner-master-key)" -eq 32
```

默认配置可直接用于断网 smoke；关键默认值如下：

| 配置 | 默认值 | 语义 |
|---|---:|---|
| `server.listen_address` | `127.0.0.1:8080` | 仅 loopback |
| `runtime.network_mode` | `none` | sandbox 默认无网络 |
| `runner.execution_uid/gid` | `1000/1000` | runner 与用户命令的固定非 root 身份 |
| `runner.default_cwd` | `/workspace` | cwd 只能解析到该目录树内 |
| `runner.default_timeout` | `10m` | `timeout_seconds=0` 时使用 |
| `runner.max_timeout` | `1h` | 单次 execution 上限 |
| `runner.termination_grace` | `2s` | SIGTERM 到 SIGKILL 的宽限期 |
| `runner.max_concurrent_executions` | `8` | 每个 sandbox 并发上限 |
| `runner.max_output_bytes` | `10485760` | 每次 execution stdout+stderr 上限 |
| `security.allow_outbound` | `false` | 服务端 outbound 总开关 |
| `egress.ready_timeout` | `30s` | sidecar attestation 等待上限 |

```bash
./bin/sandboxd -config configs/sandboxd.example.yaml
curl --fail http://127.0.0.1:8080/readyz
```

## 4. 创建 sandbox

默认断网 sandbox：

```json
{"image":"debian:bookworm-slim"}
```

```bash
curl --fail-with-body -H 'Content-Type: application/json' \
  --data '{"image":"debian:bookworm-slim"}' \
  http://127.0.0.1:8080/v1/sandboxes
```

创建返回 `202` 和 sandbox ID。继续轮询 `GET /v1/sandboxes/{sandbox_id}`，只有 state 为
`Running` 时才可执行命令；`Pending/Creating` 期间执行会返回稳定冲突错误。

## 5. 前台 execution

优先使用 `argv`，它不会经过 shell 展开；只有确实需要管道、重定向或 `&&` 时才使用 `shell`。
二者必须且只能出现一个。`cwd` 缺省为 `/workspace`，并且 symlink 解析后的最终目录也必须留在
workspace 内。`timeout_seconds` 单位为秒，零或缺省表示使用服务端默认值。

```json
{"argv":["go","test","./..."],"cwd":"/workspace","env":{"CI":"true"},"timeout_seconds":120}
```

```bash
curl --no-buffer --fail-with-body -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data '{"argv":["go","test","./..."],"cwd":"/workspace","timeout_seconds":120}' \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}/executions"
```

成功响应为 `200 text/event-stream`。事件 sequence 从 1 单调递增，stdout/stderr 分离且数据在
`data_base64` 中。每条流恰好以 `exited`、`failed`、`cancelled` 或 `timed_out` 之一结束。
用户程序非零退出仍是 `exited`，通过 `exit_code` 判断业务成功；`failed` 表示启动或 runner
内部失败。客户端在终态前断开会取消完整进程组。

shell 示例：

```json
{"shell":"go test ./... && go build ./...","cwd":"/workspace","timeout_seconds":300}
```

## 6. 后台 execution、日志和取消

后台请求设置 `background:true`，使用 `Accept: application/json`，成功返回 `202`、`Location`
以及 `execution_id`。后台 execution 不因创建请求断开而取消。

```json
{"argv":["go","test","./..."],"cwd":"/workspace","timeout_seconds":300,"background":true}
```

```bash
curl --fail-with-body -H 'Content-Type: application/json' -H 'Accept: application/json' \
  --data '{"argv":["go","test","./..."],"cwd":"/workspace","background":true}' \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}/executions"

curl --fail-with-body \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}/executions/${EXECUTION_ID}"

curl --fail-with-body \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}/executions/${EXECUTION_ID}/logs?cursor=0&limit=64"

curl --fail-with-body -X DELETE \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}/executions/${EXECUTION_ID}"
```

日志 cursor 表示客户端最后已读取的 sequence；下一页使用响应的 `next_cursor`。`complete=true`
表示该页已经包含终态，不表示日志永久保存。取消活动任务返回 `202`，取消已终态任务返回 `204`，
重复取消是安全的。timeout 与 cancel 竞争时只会有一个终态获胜。

## 7. 启用 outbound

outbound 必须同时满足三个条件：服务端 `security.allow_outbound: true`、`egress.image` 为审批过
的 digest 引用、创建请求显式设置 `network.outbound:true`。

```yaml
security:
  runner_master_key_file: "/etc/minisandbox/runner-master-key"
  allow_outbound: true

egress:
  image: "registry.example/minisandbox-egressd@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  protocol_version: 1
  ready_timeout: "30s"
  egress_denied_cidrs:
    - "203.0.113.0/24"
```

示例 digest 仅展示格式，不能用于部署。`sandboxd` 会幂等 Ensure 服务级 `minisandbox-egress`
bridge，不要求运维预创建 network。每个 outbound sandbox 有一个 egress sidecar，主容器以
`container:<sidecar-id>` 共享它的 network namespace；主容器没有 `NET_ADMIN/NET_RAW`。
sidecar 原子安装 nft policy、降为非 root anchor 并写入 attestation 后，sandbox 才能 Running。

```json
{"image":"registry.example/coding-agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","network":{"outbound":true}}
```

内置 deny 基线不可删除，覆盖 RFC1918、CGNAT、link-local、loopback、multicast、保留和文档地址；
实际 bridge subnet/gateway 也自动加入。`egress.egress_denied_cidrs` 只能追加 IPv4/IPv6 CIDR，
变更只作用于随后按新配置创建/恢复的受管资源。不要配置平台公网地址 allowlist；若必须限制公网
目的地，应使用外部显式代理或下一阶段设计，而不是把当前 CIDR deny 误当 allowlist。

每次新 execution 前，控制面都会重新核对 sidecar、network mode、policy attestation 和 runner
netns。任何漂移都 fail closed，并以 `EGRESS_UNHEALTHY`/`RUNNER_UNHEALTHY` 等安全错误拒绝新任务。

## 8. 排障

| 现象/错误码 | 检查项 |
|---|---|
| `OUTBOUND_NOT_ALLOWED` | 服务端总开关仍为 false；请求不能自行提权 |
| `EGRESS_IMAGE_UNAVAILABLE` | digest 引用在 daemon 上不可拉取、不可验证或平台不匹配 |
| `EGRESS_POLICY_INVALID` | 追加 CIDR、bridge 事实、协议版本或 policy hash 不合法 |
| `EGRESS_NOT_READY` | sidecar 未在 `ready_timeout` 内完成 nft、降权和 attestation |
| `EGRESS_UNHEALTHY` / `RUNNER_UNHEALTHY` | sidecar 停止、network mode/netns 漂移或 runner 不可达；先停止接收新任务 |
| `INVALID_CWD` | cwd 不存在、不是目录、经 symlink 逃出 `/workspace` 或权限不足 |
| `SHELL_NOT_FOUND` | 镜像没有可执行的 `/bin/bash` 或 `/bin/sh`；改用 argv 或修复镜像 |
| `EXECUTION_LIMIT_REACHED` | 同 sandbox 活动 execution 达到 8（或配置值）；等待或取消任务 |
| `TimedOut` | 调整命令或在不超过 `runner.max_timeout` 的范围内增加超时 |

诊断时只记录公开错误码、request ID、sandbox/execution ID、状态和受管资源摘要。不要输出请求
环境变量、命令正文、stdout/stderr、runner token、主密钥、Docker host 路径或 attestation 原文。
同名非受管 `minisandbox-egress` network、sidecar label/config 漂移均应先人工确认归属，不能用
宽泛名称匹配批量删除。

## 9. 清理和验收

```bash
curl --fail-with-body -X DELETE \
  "http://127.0.0.1:8080/v1/sandboxes/${SANDBOX_ID}"
go test ./...
go vet ./...
```

DELETE 是异步幂等操作；轮询 sandbox 到 `Terminated`。成功清理包括主容器、egress sidecar、
非持久 workspace、runner socket、runtime directory 和 execution 日志。原生 Linux Docker 的
完整 opt-in 命令、digest 镜像变量和无公网 coding workflow 见
[Integration tests](../tests/integration/README.md)。
