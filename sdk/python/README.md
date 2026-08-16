# MiniSandbox Python SDK

面向 MiniSandbox 的同步 Python 客户端，纯标准库、零依赖。WebSocket（PTY）
为按 RFC 6455 实现的最小客户端，仅连接本服务的 `minisandbox.pty.v1`。

## 安装与测试

SDK 位于仓库 `sdk/python`，直接加入 `sys.path` 使用：

```python
import sys
sys.path.insert(0, "path/to/minisandbox/sdk/python")

from minisandbox import Client
```

```bash
cd sdk/python && python3 -m unittest discover -s tests
```

## 快速开始

```python
from minisandbox import Client

client = Client("http://127.0.0.1:8080")
sandbox = client.create("debian:bookworm-slim")
try:
    _, capabilities = sandbox.wait_ready()

    sandbox.files().upload("src/build.sh", source, create_parents=True)
    result = sandbox.run(argv=["/bin/sh", "/workspace/src/build.sh"])
    artifact = sandbox.files().download("artifact.txt")
finally:
    sandbox.delete()
```

## 功能总览

| 对象 | 方法 | 说明 |
|---|---|---|
| `Client` | `create` / `sandbox` / `health` / `readiness` | 创建与绑定资源、服务探测 |
| `Sandbox` | `info` / `wait_running` / `wait_ready` / `renew` / `delete` / `delete_and_wait` | 生命周期 |
| | `run` / `start_execution` | 一次调用执行或后台执行 |
| | `capabilities` / `files()` / `open_pty()` / `port_http()` | Agent 功能 |
| `Execution` | `info` / `wait` / `cancel_and_wait` / `log_page` / `collect_logs` | 后台执行管理 |
| `SandboxFiles` | `stat` / `list` / `mkdir` / `upload` / `download` / `move` / `delete` | workspace 文件 |
| `PTYConnection` | `write` / `resize` / `read_output` / `wait_terminal` / `close` | 交互终端 |
| `PortHTTP` | `request` | 访问 sandbox 内 loopback HTTP 服务 |

错误统一为 `ResponseError`（`status` / `code` / `request_id` / `retryable`）；
`run` 的非成功终态抛出携带 `result` 字典的 `RunError`。完整示例见
[examples/python/agent.py](../../examples/python/agent.py)。
