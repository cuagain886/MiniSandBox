# MiniSandbox TypeScript SDK

面向 Node.js（≥22）的 MiniSandbox 客户端，零运行时依赖，仅使用原生
`fetch` 与 `WebSocket`。在仓库内使用前先构建：

```bash
cd sdk/typescript && npm install && npm run build && npm test
```

## 快速开始

```ts
import { Client } from "minisandbox-sdk";

const client = new Client("http://127.0.0.1:8080");
const sandbox = await client.create({ image: "debian:bookworm-slim" });
try {
  const { capabilities } = await sandbox.waitReady();

  await sandbox.files().upload("src/build.sh", source, { createParents: true });
  const result = await sandbox.run({ argv: ["/bin/sh", "/workspace/src/build.sh"] });
  const artifact = await sandbox.files().download("artifact.txt");
} finally {
  await sandbox.delete();
}
```

## 功能总览

| 对象 | 方法 | 说明 |
|---|---|---|
| `Client` | `create` / `sandbox` / `health` / `readiness` | 创建与绑定资源、服务探测 |
| `Sandbox` | `info` / `waitRunning` / `waitReady` / `renew` / `delete` / `deleteAndWait` | 生命周期 |
| | `run` / `startExecution` | 一次调用执行或后台执行 |
| | `capabilities` | 查询 files/PTY/port proxy 能力 |
| | `files()` / `openPTY()` / `portHTTP()` | 返回 Agent 功能对象 |
| `Execution` | `info` / `wait` / `cancelAndWait` / `logPage` / `collectLogs` | 后台执行管理 |
| `SandboxFiles` | `stat` / `list` / `mkdir` / `upload` / `download` / `move` / `remove` | workspace 文件 |
| `PTYConnection` | `write` / `resize` / `readOutput` / `waitTerminal` / `close` | 交互终端 |
| `PortHTTP` | `request` | 访问 sandbox 内 loopback HTTP 服务 |

错误统一为 `ResponseError`（`status` / `code` / `requestId` / `retryable`）；
`run` 的非成功终态抛出携带 `result` 的 `RunError`。完整示例见
[examples/typescript/agent.ts](../../examples/typescript/agent.ts)。
