# MiniSandbox

> 全 Go 实现的 AI Agent 命令沙盒运行时 —— 让 Agent 在受控的 Docker 容器中安全地执行命令。

![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Docker-2496ED?logo=docker&logoColor=white)
![Status](https://img.shields.io/badge/Status-Prototype-orange)

[English](README.md) | 简体中文

MiniSandbox 是一个单机沙盒控制面:你告诉它"给我一个沙盒、在里面跑这条命令、到点回收",它负责创建容器、注入执行组件、执行命令、回收资源,并保证中途崩溃也能恢复或清理干净。任意用户镜像开箱即用,无需预装任何组件。

## 功能

- **沙盒生命周期**:异步创建/查询/删除 —— 幂等、失败自动补偿、控制面崩溃重启可恢复。
- **命令执行**:`argv` 或 `shell`,前台 SSE 流式输出,后台任务(状态/日志/取消);超时与取消按完整进程组终止。
- **租约与可靠性**:TTL 到期自动回收、续期、幂等创建、配额限制。
- **安全默认**:命令以非 root 身份执行;容器默认断网(`network=none`)、`CapDrop=ALL`、CPU/内存/进程数限额;出站网络只能通过显式开启的受管 egress sidecar。
- **Agent 体验（Phase 4）**：workspace 文件（上传/下载/移动/删除）、WebSocket 交互终端、loopback HTTP 端口代理，以及 [Go](sdk/go/)、[TypeScript](sdk/typescript/)、[Python](sdk/python/) 三语言 SDK。
- **Go SDK**:见 [`sdk/go`](sdk/go/)。

## 快速开始

### 环境要求

- Linux/amd64 + 可访问的 Docker Engine(能拉取 `debian:bookworm-slim`)
- Go 1.26+、GNU Make、`curl`、`jq`

> Windows 开发机可以编译和跑单测,但沙盒运行时本身只面向 Linux;完整行为需在 Linux(如 WSL2)中验证。

### 构建与启动

```bash
# 产出 bin/sandboxd、bin/runnerd、bin/sandbox-init
make build

# runner master key:恰好 32 字节、权限 0600 的普通文件
sudo mkdir -p /etc/minisandbox
head -c 32 /dev/urandom | sudo tee /etc/minisandbox/runner-master-key >/dev/null
sudo chmod 600 /etc/minisandbox/runner-master-key

sudo ./bin/sandboxd --config configs/sandboxd.example.yaml
# 另一个终端确认就绪
curl -s http://127.0.0.1:8080/readyz | jq .
```

### 使用沙盒

```bash
# 1. 创建(异步,返回 202;记下返回的 ID)
curl -s -X POST http://127.0.0.1:8080/v1/sandboxes \
  -H 'Content-Type: application/json' \
  -d '{"image":"debian:bookworm-slim","ttl_seconds":1800}' | jq .

# 2. 轮询直到 state 变为 "Running"(把 <id> 换成上一步的 ID)
curl -s http://127.0.0.1:8080/v1/sandboxes/<id> | jq .

# 3. 前台执行:SSE 流式返回 stdout/stderr 事件
curl -N http://127.0.0.1:8080/v1/sandboxes/<id>/executions \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"argv":["sh","-c","echo hello from sandbox"]}'

# 4. 后台执行:立即返回 execution 描述符,可查状态、分页日志、取消
curl -s http://127.0.0.1:8080/v1/sandboxes/<id>/executions \
  -H 'Content-Type: application/json' \
  -d '{"shell":"sleep 60 && echo done","background":true}' | jq .
curl -s http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id> | jq .
curl -s "http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id>/logs?cursor=0" | jq .
curl -s -X DELETE http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id>

# 5. 续期与删除(TTL 到期也会自动删除;重复删除幂等)
curl -s -X POST http://127.0.0.1:8080/v1/sandboxes/<id>/renew \
  -H 'Content-Type: application/json' \
  -d '{"expires_at":"2026-08-15T12:00:00Z"}' | jq .
curl -s -X DELETE http://127.0.0.1:8080/v1/sandboxes/<id> -o /dev/null -w '%{http_code}\n'
```

### Go SDK

推荐使用高层资源 API（见 [`sdk/go/README.md`](sdk/go/README.md)）：
创建 sandbox、等待就绪、一次调用执行命令并删除，无需手写轮询、游标或 Base64 解码。

```go
package main

import (
	"context"
	"fmt"
	"time"

	"minisandbox/sdk/go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := sdk.NewClient("http://127.0.0.1:8080", nil)

	sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{
		Image: "debian:bookworm-slim",
	}, sdk.WithIdempotencyKey("demo-create-1"))
	if err != nil {
		panic(err)
	}
	defer sandbox.Delete(context.Background())

	if _, err := sandbox.WaitRunning(ctx); err != nil {
		panic(err)
	}

	result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv:    []string{"sh", "-c", "echo hello"},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(string(result.Stdout), result.ExitCode)
}
```
