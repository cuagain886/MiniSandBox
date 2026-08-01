# Phase 1 Docker 生命周期运行与手工验收

本文用于在一台干净的 Linux/amd64 Docker 主机上构建并手工验收
MiniSandbox Phase 1。Phase 1 只提供 sandbox 的创建、查询和删除，不提供用户
命令执行、SSE、取消、PTY、文件或网络 API。

## 1. 安全边界和环境要求

准备以下工具：

- Linux/amd64；
- Go 1.26 或更高的兼容版本；
- 可访问的 Docker Engine；
- GNU Make、`curl`、`jq` 和 `sudo`。

`sandboxd` 默认只监听 loopback，不能直接作为公网服务。它作为宿主机控制面
需要访问 Docker Engine；Docker socket 只供 `sandboxd` 使用，绝不能挂载进
sandbox 容器。Phase 1 创建的 sandbox 固定使用 `network=none`，不需要
`--privileged`、host network 或额外的宿主机端口。

当前 Phase 1 把每个 sandbox 的 runner runtime directory 收敛为 `0700`，
容器内负责创建 Unix Socket 的 init/runner 使用 UID 0。为保证 bind mount
两侧的目录属主一致，下面的手工验收显式以 root 启动 `sandboxd`。仅把普通用户
加入 `docker` 组虽然足以调用 Docker API，但不足以让容器访问该用户拥有的
`0700` runtime directory。不要用 `chmod 777` 或额外 capability 绕过此约束。

下面的命令都在仓库根目录执行。示例使用 `mktemp` 创建一次性数据目录，以免
覆盖已有环境：

```bash
export MS_ROOT="$(mktemp -d /tmp/minisandbox-phase1.XXXXXX)"
export MS_URL="http://127.0.0.1:18080"
```

Unix Socket 的完整路径在 Linux 上有长度限制，因此生产部署也应选择
`/var/lib/minisandbox` 这类较短的绝对数据路径。

## 2. 构建 Linux/amd64 产物

```bash
make build
file bin/sandboxd bin/runnerd bin/sandbox-init
```

`make build` 先静态构建 `runnerd` 和 `sandbox-init`，再把两者嵌入
`sandboxd`。不要手工编辑 `internal/embedded/artifacts/` 中的二进制。

## 3. 写入本次验收配置

```bash
cat >"$MS_ROOT/sandboxd.yaml" <<EOF
server:
  listen_address: "127.0.0.1:18080"
  shutdown_timeout: "10s"

data:
  directory: "$MS_ROOT"
  sqlite_path: "$MS_ROOT/sandboxd.db"

runtime:
  type: "docker"
  docker_host: "unix:///var/run/docker.sock"
  default_image: "debian:bookworm-slim"
  runner_socket_directory: "$MS_ROOT/run"
  workspace_directory: "$MS_ROOT/workspaces"
  network_mode: "none"
  workspace_persistent: false
  platform:
    os: "linux"
    arch: "amd64"

reconcile:
  interval: "2s"
  runner_ready_timeout: "30s"
  deletion_timeout: "60s"

limits:
  default_ttl: "30m"
  maximum_ttl: "24h"
  default_resources:
    cpu_quota_millis: 500
    memory_mib: 512
    pids: 128
  max_resources:
    cpu_quota_millis: 4000
    memory_mib: 8192
    pids: 1024
EOF
```

## 4. 启动和检查控制面

```bash
start_sandboxd() {
  if ! sudo -v || ! sudo -n true; then
    echo "sudo authentication is required to start sandboxd" >&2
    return 1
  fi

  sudo -n ./bin/sandboxd -config "$MS_ROOT/sandboxd.yaml" \
    >"$MS_ROOT/sandboxd.log" 2>&1 &
  export SANDBOXD_PID=$!
  trap 'sudo -n kill "$SANDBOXD_PID" 2>/dev/null || true' EXIT

  HEALTHY=false
  for attempt in $(seq 1 30); do
    if curl -fsS --max-time 2 "$MS_URL/healthz" \
      >"$MS_ROOT/health.json" 2>/dev/null; then
      HEALTHY=true
      break
    fi
    if ! sudo -n kill -0 "$SANDBOXD_PID" 2>/dev/null; then
      echo "sandboxd exited before /healthz became available" >&2
      tail -n 100 "$MS_ROOT/sandboxd.log" >&2
      trap - EXIT
      return 1
    fi
    sleep 1
  done
  if [ "$HEALTHY" != true ]; then
    echo "timed out waiting for /healthz" >&2
    tail -n 100 "$MS_ROOT/sandboxd.log" >&2
    sudo -n kill "$SANDBOXD_PID" 2>/dev/null || true
    wait "$SANDBOXD_PID" 2>/dev/null || true
    trap - EXIT
    return 1
  fi
  jq . "$MS_ROOT/health.json"

  READY=false
  for attempt in $(seq 1 60); do
    if curl -fsS --max-time 2 "$MS_URL/readyz" \
        >"$MS_ROOT/ready.json" 2>/dev/null &&
      jq -e '.status == "ready"' "$MS_ROOT/ready.json" >/dev/null; then
      READY=true
      break
    fi
    if ! sudo -n kill -0 "$SANDBOXD_PID" 2>/dev/null; then
      echo "sandboxd exited while waiting for /readyz" >&2
      tail -n 100 "$MS_ROOT/sandboxd.log" >&2
      trap - EXIT
      return 1
    fi
    sleep 1
  done
  if [ "$READY" != true ]; then
    echo "timed out waiting for /readyz" >&2
    test ! -s "$MS_ROOT/ready.json" || jq . "$MS_ROOT/ready.json" >&2
    tail -n 100 "$MS_ROOT/sandboxd.log" >&2
    sudo -n kill "$SANDBOXD_PID" 2>/dev/null || true
    wait "$SANDBOXD_PID" 2>/dev/null || true
    trap - EXIT
    return 1
  fi
  jq . "$MS_ROOT/ready.json"
}

start_sandboxd
```

`/healthz` 只表示 HTTP 进程存活；`/readyz` 成功才表示 Store、Docker、嵌入
artifact、启动恢复和 reconcile worker 均已就绪。后台任务取得 PID 时 HTTP
listener 不一定已经完成绑定，因此示例先有限等待 `/healthz`，再等待
`/readyz`；任一步超时或进程提前退出都会显示 `sandboxd.log`，不会无限循环。
启动流程封装在 shell 函数中，失败时只返回非零状态，不会退出当前交互式 shell。
由于 `sandboxd` 由 root 启动，进程存活检查同样使用
`sudo -n kill -0`；普通用户直接执行 `kill -0` 会因 `EPERM` 把存活进程误判为
已经退出。

## 5. 创建并等待 Running

```bash
create_sandbox() {
curl -fsS \
  -D "$MS_ROOT/create.headers" \
  -o "$MS_ROOT/create.json" \
  -X POST "$MS_URL/v1/sandboxes" \
  -H 'Content-Type: application/json' \
  --data '{"image":"debian:bookworm-slim"}'

cat "$MS_ROOT/create.headers"
jq . "$MS_ROOT/create.json"
export SANDBOX_ID="$(jq -er '.id' "$MS_ROOT/create.json")"

RUNNING=false
for attempt in $(seq 1 180); do
  curl -fsS --max-time 5 "$MS_URL/v1/sandboxes/$SANDBOX_ID" \
    -o "$MS_ROOT/sandbox.json"
  STATE="$(jq -r '.state' "$MS_ROOT/sandbox.json")"
  REASON="$(jq -r '.reason' "$MS_ROOT/sandbox.json")"
  printf 'attempt=%s state=%s reason=%s\n' "$attempt" "$STATE" "$REASON"
  case "$STATE" in
    Running)
      RUNNING=true
      break
      ;;
    Failed)
      jq . "$MS_ROOT/sandbox.json"
      return 1
      ;;
  esac
  if ! sudo -n kill -0 "$SANDBOXD_PID" 2>/dev/null; then
    echo "sandboxd exited while creating the sandbox" >&2
    tail -n 100 "$MS_ROOT/sandboxd.log" >&2
    return 1
  fi
  sleep 1
done
if [ "$RUNNING" != true ]; then
  echo "timed out waiting for sandbox Running" >&2
  jq . "$MS_ROOT/sandbox.json" >&2
  tail -n 100 "$MS_ROOT/sandboxd.log" >&2
  return 1
fi
jq . "$MS_ROOT/sandbox.json"
}

create_sandbox
```

创建响应应为 `202 Accepted`，且带
`Location: /v1/sandboxes/<id>`。容器启动只是中间状态；只有独立 Unix Socket
上的 runner 健康检查成功后，资源才进入 `Running`。

可使用只读 inspect 检查关键边界：

```bash
docker ps --filter "label=minisandbox.io/id=$SANDBOX_ID"
docker inspect \
  "$(docker ps -aq --filter "label=minisandbox.io/id=$SANDBOX_ID")" \
  --format '{{json .HostConfig.NetworkMode}} {{json .HostConfig.Privileged}} {{json .Config.ExposedPorts}}'
docker inspect \
  "$(docker ps -aq --filter "label=minisandbox.io/id=$SANDBOX_ID")" \
  --format '{{json .Config.Labels}}' | jq .
```

期望网络模式为 `"none"`、privileged 为 `false`、没有 exposed ports。
MiniSandbox 自己拥有的 `minisandbox.io/*` label 命名空间只包含已审查的
`managed`、`id`、`schema-version`、`spec-hash`、`expires-at` 和 `workspace`。
Docker Desktop 或宿主机运维系统可能附加其他命名空间的 label，例如
`desktop.docker.io/wsl-distro`；这类平台元数据不属于 MiniSandbox 恢复协议，
但仍不得包含 runner token、用户环境变量、凭据、命令、输出或请求正文。

## 6. 删除、幂等重试和正常清理

```bash
delete_sandbox() {
curl -sS -o /dev/null -w 'DELETE status=%{http_code}\n' \
  -X DELETE "$MS_URL/v1/sandboxes/$SANDBOX_ID"

TERMINATED=false
for attempt in $(seq 1 90); do
  curl -fsS --max-time 5 "$MS_URL/v1/sandboxes/$SANDBOX_ID" \
    -o "$MS_ROOT/sandbox.json"
  STATE="$(jq -r '.state' "$MS_ROOT/sandbox.json")"
  REASON="$(jq -r '.reason' "$MS_ROOT/sandbox.json")"
  printf 'attempt=%s state=%s reason=%s\n' "$attempt" "$STATE" "$REASON"
  case "$STATE" in
    Terminated)
      TERMINATED=true
      break
      ;;
    Failed)
      jq . "$MS_ROOT/sandbox.json"
      return 1
      ;;
  esac
  if ! sudo -n kill -0 "$SANDBOXD_PID" 2>/dev/null; then
    echo "sandboxd exited while deleting the sandbox" >&2
    tail -n 100 "$MS_ROOT/sandboxd.log" >&2
    return 1
  fi
  sleep 1
done
if [ "$TERMINATED" != true ]; then
  echo "timed out waiting for sandbox Terminated" >&2
  jq . "$MS_ROOT/sandbox.json" >&2
  tail -n 100 "$MS_ROOT/sandboxd.log" >&2
  return 1
fi

curl -sS -o /dev/null -w 'repeated DELETE status=%{http_code}\n' \
  -X DELETE "$MS_URL/v1/sandboxes/$SANDBOX_ID"
test -z "$(docker ps -aq --filter "label=minisandbox.io/id=$SANDBOX_ID")"
test -z "$(docker volume ls -q --filter "label=minisandbox.io/id=$SANDBOX_ID")"
test ! -e "$MS_ROOT/run/$SANDBOX_ID"

sudo -n kill "$SANDBOXD_PID"
wait "$SANDBOXD_PID"
trap - EXIT
case "$MS_ROOT" in
  /tmp/minisandbox-phase1.*) sudo -n rm -rf -- "$MS_ROOT" ;;
  *) echo "refusing to remove unexpected MS_ROOT: $MS_ROOT" >&2; return 1 ;;
esac
}

delete_sandbox
```

第一次 DELETE 通常返回 `202`，重复 DELETE 对已经终止的资源返回 `204`。
`Terminated` 表示受管容器、非持久 workspace volume 和 runtime directory 已
完成清理。

## 7. 异常退出后的定向清理

应优先重启同一 `sandboxd`，让 reconcile 根据 SQLite 与 labels 恢复并处理
DELETE。只有控制面无法恢复时，才使用已知 sandbox ID 定向清理，不能按名称
前缀删除：

```bash
docker ps -aq --filter "label=minisandbox.io/id=$SANDBOX_ID" |
  while IFS= read -r container_id; do
    test -z "$container_id" || docker rm -f "$container_id"
  done

docker volume ls -q --filter "label=minisandbox.io/id=$SANDBOX_ID" |
  while IFS= read -r volume_name; do
    test -z "$volume_name" || docker volume rm "$volume_name"
  done

rm -rf -- "$MS_ROOT/run/$SANDBOX_ID"
```

这些命令只处理精确的 `minisandbox.io/id`。不要使用
`docker system prune`、`minisandbox-*` 名称通配符或强制删除未知 volume。

## 8. Phase 1 已知限制

- 只支持 Linux/amd64 artifact；
- 只提供生命周期 API，不能执行用户命令；
- 只绑定 loopback，没有 API Key、RBAC 或多租户隔离；
- sandbox 无网络，workspace 在删除时一并清理；
- 不支持 TTL 自动回收、续期、幂等创建、Pool、快照或 Kubernetes；
- 第一版依赖 Docker 容器边界，不启用 nested bubblewrap。

自动化验收方式见
[Integration tests](../../tests/integration/README.md)，最终证据见
[Phase 1 验收报告](../reports/phase1-acceptance.md)。
