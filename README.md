# MiniSandbox

> An all-Go sandbox runtime for AI agents — run agent commands safely inside controlled Docker containers.

![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Docker-2496ED?logo=docker&logoColor=white)
![Status](https://img.shields.io/badge/Status-Prototype-orange)

English | [简体中文](README-zh.md)

MiniSandbox is a single-host sandbox control plane: you tell it "give me a sandbox, run this command in it, and reclaim it when it expires" — it creates the container, injects its own execution components, runs the command, and reclaims resources, guaranteeing recovery or cleanup even if it crashes midway. Works with any user image out of the box, with nothing preinstalled.

## Features

- **Sandbox lifecycle**: asynchronous create/get/delete — idempotent, with automatic failure compensation and control-plane crash recovery.
- **Command execution**: `argv` or `shell`, foreground SSE streaming output, background tasks (status/logs/cancel); timeout and cancellation kill the whole process group.
- **Leases and reliability**: TTL-based automatic expiry, renewal, idempotent creation, and quota limits.
- **Secure defaults**: commands run as a non-root user; containers are network-isolated by default (`network=none`) with `CapDrop=ALL` and CPU/memory/PID limits; outbound network is only possible through an explicitly enabled managed egress sidecar.
- **Agent experience (Phase 4)**: workspace files (upload/download/move/delete), interactive PTY over WebSocket, loopback HTTP port proxy, and SDKs for [Go](sdk/go/), [TypeScript](sdk/typescript/), and [Python](sdk/python/).
- **Go SDK**: see [`sdk/go`](sdk/go/).

## Quick Start

### Requirements

- Linux/amd64 with an accessible Docker Engine (able to pull `debian:bookworm-slim`)
- Go 1.26+, GNU Make, `curl`, `jq`

> Windows dev machines can compile the code and run unit tests, but the sandbox runtime itself targets Linux only; verify full behavior on Linux (e.g. WSL2).

### Build & Run

```bash
# Produces bin/sandboxd, bin/runnerd, bin/sandbox-init
make build

# Runner master key: a regular file with exactly 32 bytes, mode 0600
sudo mkdir -p /etc/minisandbox
head -c 32 /dev/urandom | sudo tee /etc/minisandbox/runner-master-key >/dev/null
sudo chmod 600 /etc/minisandbox/runner-master-key

sudo ./bin/sandboxd --config configs/sandboxd.example.yaml
# Check readiness in another terminal
curl -s http://127.0.0.1:8080/readyz | jq .
```

### Using a Sandbox

```bash
# 1. Create (async, returns 202; note the returned ID)
curl -s -X POST http://127.0.0.1:8080/v1/sandboxes \
  -H 'Content-Type: application/json' \
  -d '{"image":"debian:bookworm-slim","ttl_seconds":1800}' | jq .

# 2. Poll until state becomes "Running" (replace <id> with the ID above)
curl -s http://127.0.0.1:8080/v1/sandboxes/<id> | jq .

# 3. Foreground execution: streams stdout/stderr events over SSE
curl -N http://127.0.0.1:8080/v1/sandboxes/<id>/executions \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"argv":["sh","-c","echo hello from sandbox"]}'

# 4. Background execution: returns an execution descriptor immediately;
#    query status, fetch paginated logs, or cancel
curl -s http://127.0.0.1:8080/v1/sandboxes/<id>/executions \
  -H 'Content-Type: application/json' \
  -d '{"shell":"sleep 60 && echo done","background":true}' | jq .
curl -s http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id> | jq .
curl -s "http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id>/logs?cursor=0" | jq .
curl -s -X DELETE http://127.0.0.1:8080/v1/sandboxes/<id>/executions/<exec_id>

# 5. Renew and delete (TTL expiry also deletes automatically; repeated deletes are idempotent)
curl -s -X POST http://127.0.0.1:8080/v1/sandboxes/<id>/renew \
  -H 'Content-Type: application/json' \
  -d '{"expires_at":"2026-08-15T12:00:00Z"}' | jq .
curl -s -X DELETE http://127.0.0.1:8080/v1/sandboxes/<id> -o /dev/null -w '%{http_code}\n'
```

## Agent Features (Phase 4)

After a sandbox reaches `Running`, query its capabilities, then use files, PTY, and the loopback HTTP proxy through the public API or any of the three SDKs.

```bash
# What can this sandbox do?
curl -s http://127.0.0.1:8080/v1/sandboxes/<id>/capabilities | jq .

# Upload a file into the workspace (atomic; parents created on demand)
curl -s -X PUT "http://127.0.0.1:8080/v1/sandboxes/<id>/files/content?path=src/main.go&create_parents=true"   -H 'Content-Type: application/octet-stream' --data-binary @main.go | jq .

# List a directory and download an artifact
curl -s -X POST http://127.0.0.1:8080/v1/sandboxes/<id>/directories/list   -H 'Content-Type: application/json' -d '{"path":"."}' | jq .
curl -s "http://127.0.0.1:8080/v1/sandboxes/<id>/files/content?path=artifact.txt" -o artifact.txt

# Interactive PTY over WebSocket (subprotocol minisandbox.pty.v1): first text
# frame is {"type":"start","argv":["/bin/bash"],...}; then binary frames are
# stdin, text frames are resize, server frames carry merged terminal output.

# Reach an HTTP service listening inside the sandbox (loopback only)
curl -s "http://127.0.0.1:8080/v1/sandboxes/<id>/ports/8080/http/hello"
```

All file paths are workspace-relative and cannot escape `/workspace`; the port
proxy always dials `127.0.0.1:<port>` inside the sandbox and strips control-plane
credentials. Prefer the SDKs — see [`sdk/go`](sdk/go/README.md),
[`sdk/typescript`](sdk/typescript/README.md), and [`sdk/python`](sdk/python/README.md),
or run the shared workflow examples under [`examples/`](examples/).

Frequently used images can be pre-pulled at startup:

```yaml
runtime:
  prepull_images:
    - image: "debian:bookworm-slim"
      platform: "linux/amd64"
```

### Go SDK
The recommended path is the high-level resource API ([`sdk/go/README.md`](sdk/go/README.md)):
create a sandbox, wait until running, run a command in one call, then delete — no hand-written
polling, cursors, or Base64 decoding.

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
