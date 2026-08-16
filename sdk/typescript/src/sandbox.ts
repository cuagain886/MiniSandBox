/**
 * Sandbox 资源对象：生命周期与命令执行。
 */
import { ResponseError } from "./errors.js";
import { SandboxFiles } from "./files.js";
import { PTYConnection } from "./pty.js";
import { PortHTTP } from "./portproxy.js";
import { sandboxBasePath, sleep, Transport } from "./transport.js";
import {
  Capabilities,
  CreateSandboxRequest,
  PTYStartRequest,
  ExecutionEvent,
  ExecutionInfo,
  ExecutionLogPage,
  ExecuteRequest,
  isTerminalExecutionState,
  RunResult,
  SandboxInfo,
} from "./wire.js";

interface WireSandbox {
  id: string;
  state: SandboxInfo["state"];
  reason: string;
  message: string;
  image: string;
  expires_at: string;
  created_at: string;
  updated_at: string;
}

interface WireExecutionDescriptor {
  execution_id: string;
  state: ExecutionInfo["state"];
}

interface WireExecutionStatus {
  execution_id: string;
  state: ExecutionInfo["state"];
  terminal_event?: WireExecutionEvent;
}

interface WireExecutionEvent {
  execution_id: string;
  sequence: number;
  timestamp: string;
  type: string;
  data_base64?: string;
  exit_code?: number;
  duration_ms?: number;
  output_truncated?: boolean;
  error_code?: string;
  message?: string;
}

interface WireLogPage {
  events: WireExecutionEvent[];
  next_cursor: number;
  complete: boolean;
}

/** Client 是 MiniSandbox 生命周期 API 的 TypeScript 客户端。 */
export class Client {
  private readonly transport: Transport;

  constructor(baseUrl: string, options: { fetchImpl?: typeof fetch; pollIntervalMs?: number } = {}) {
    this.transport = new Transport({
      baseUrl,
      fetchImpl: options.fetchImpl,
      pollIntervalMs: options.pollIntervalMs,
    });
  }

  /** 创建 sandbox 并返回资源对象；可选幂等 key 支持安全重放。 */
  async create(
    request: CreateSandboxRequest,
    options: { idempotencyKey?: string } = {},
  ): Promise<Sandbox> {
    const headers: Record<string, string> = {};
    if (options.idempotencyKey) {
      headers["Idempotency-Key"] = options.idempotencyKey;
    }
    const wire: Record<string, unknown> = { image: request.image };
    if (request.ttlSeconds !== undefined) {
      wire.ttl_seconds = request.ttlSeconds;
    }
    if (request.network) {
      wire.network = { outbound: request.network.outbound };
    }
    const { value } = await this.transport.requestJSON<WireSandbox>(
      "POST",
      "/v1/sandboxes",
      wire,
      { headers },
    );
    return new Sandbox(this.transport, value.id);
  }

  /** 用已知 ID 绑定资源对象；不发起请求。 */
  sandbox(sandboxId: string): Sandbox {
    return new Sandbox(this.transport, sandboxId);
  }

  /** 查询控制面存活状态。 */
  async health(): Promise<void> {
    await this.transport.requestJSON<unknown>("GET", "/healthz");
  }

  /** 查询控制面与必要组件的就绪状态；未就绪不视为错误。 */
  async readiness(): Promise<{ ready: boolean; components: Array<{ name: string; ready: boolean }> }> {
    let value: { status: string; components: Array<{ name: string; status: string }> };
    try {
      const decoded = await this.transport.requestJSON<typeof value>("GET", "/readyz");
      value = decoded.value;
    } catch (error) {
      if (error instanceof ResponseError && error.status === 503) {
        return { ready: false, components: [] };
      }
      throw error;
    }
    return {
      ready: value.status === "ready",
      components: value.components.map((component) => ({
        name: component.name,
        ready: component.status === "ready",
      })),
    };
  }
}

/** Sandbox 表示一个 sandbox 资源；本身不缓存状态。 */
export class Sandbox {
  constructor(
    private readonly transport: Transport,
    readonly id: string,
  ) {}

  /** 查询当前生命周期状态。 */
  async info(): Promise<SandboxInfo> {
    const { value } = await this.transport.requestJSON<WireSandbox>(
      "GET",
      sandboxBasePath(this.id),
    );
    return wireToSandboxInfo(value);
  }

  /** 轮询至 Running；Failed 或提前 Terminated 时抛错。 */
  async waitRunning(timeoutMs = 90_000): Promise<SandboxInfo> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const info = await this.info();
      if (info.state === "Running") {
        return info;
      }
      if (info.state === "Failed" || info.state === "Terminated") {
        throw new Error(`sandbox ${this.id} entered ${info.state}: ${info.reason}: ${info.message}`);
      }
      if (Date.now() > deadline) {
        throw new Error(`timeout waiting for sandbox ${this.id} to become Running`);
      }
      await sleep(this.transport.pollIntervalMs);
    }
  }

  /** 延长租约到绝对时间。 */
  async renew(expiresAt: Date): Promise<SandboxInfo> {
    const { value } = await this.transport.requestJSON<WireSandbox>(
      "POST",
      `${sandboxBasePath(this.id)}/renew`,
      { expires_at: expiresAt.toISOString() },
    );
    return wireToSandboxInfo(value);
  }

  /** 提交删除意图并立即返回。 */
  async delete(): Promise<void> {
    await this.transport.requestJSON<unknown>("DELETE", sandboxBasePath(this.id));
  }

  /** 删除并等待 Terminated。 */
  async deleteAndWait(timeoutMs = 90_000): Promise<SandboxInfo> {
    await this.delete();
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const info = await this.info();
      if (info.state === "Terminated") {
        return info;
      }
      if (info.state === "Failed") {
        throw new Error(`sandbox ${this.id} failed during deletion: ${info.reason}`);
      }
      if (Date.now() > deadline) {
        throw new Error(`timeout waiting for sandbox ${this.id} to terminate`);
      }
      await sleep(this.transport.pollIntervalMs);
    }
  }

  /** 查询当前 sandbox 的功能能力。 */
  async capabilities(): Promise<Capabilities> {
    const { value } = await this.transport.requestJSON<Capabilities>(
      "GET",
      `${sandboxBasePath(this.id)}/capabilities`,
    );
    return value;
  }

  /** 等待 Running 并确认能力可用，返回二者结果。 */
  async waitReady(timeoutMs = 90_000): Promise<{ info: SandboxInfo; capabilities: Capabilities }> {
    const info = await this.waitRunning(timeoutMs);
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      try {
        const capabilities = await this.capabilities();
        return { info, capabilities };
      } catch (error) {
        if (!(error instanceof ResponseError) || !error.retryable) {
          throw error;
        }
      }
      if (Date.now() > deadline) {
        throw new Error(`timeout waiting for sandbox ${this.id} capabilities`);
      }
      await sleep(this.transport.pollIntervalMs);
    }
  }

  /** 返回 workspace 文件管理对象。 */
  files(): SandboxFiles {
    return new SandboxFiles(this.transport, this.id);
  }

  /** 打开一个交互式 PTY 会话。 */
  async openPTY(request: PTYStartRequest): Promise<PTYConnection> {
    return PTYConnection.open(this.transport, this.id, request);
  }

  /** 返回 sandbox loopback HTTP 代理对象。 */
  portHTTP(): PortHTTP {
    return new PortHTTP(this.transport, this.id);
  }

  /** 启动后台 execution 并返回资源对象。 */
  async startExecution(request: ExecuteRequest): Promise<Execution> {
    const { value } = await this.transport.requestJSON<WireExecutionDescriptor>(
      "POST",
      `${sandboxBasePath(this.id)}/executions`,
      toWireExecuteRequest(request, true),
    );
    return new Execution(this.transport, this.id, value.execution_id);
  }

  /** 一次调用完成执行并收集输出；非零退出等终态同时返回结果与错误。 */
  async run(request: ExecuteRequest): Promise<RunResult> {
    const execution = await this.startExecution(request);
    const info = await execution.wait();
    const logs = await execution.collectLogs();
    const result: RunResult = {
      executionId: execution.id,
      state: info.state,
      exitCode: info.terminalEvent?.exitCode ?? -1,
      stdout: logs.stdout,
      stderr: logs.stderr,
      durationMs: info.terminalEvent?.durationMs ?? 0,
      outputTruncated: info.terminalEvent?.outputTruncated ?? false,
    };
    if (info.state !== "Exited" || result.exitCode !== 0) {
      const detail = `execution ${execution.id} ${info.state}` +
        (info.terminalEvent?.exitCode !== undefined ? ` (exit ${info.terminalEvent.exitCode})` : "");
      throw new RunError(result, detail);
    }
    return result;
  }
}

/** RunError 携带已收集输出的终态错误。 */
export class RunError extends Error {
  constructor(readonly result: RunResult, detail: string) {
    super(detail);
    this.name = "MiniSandboxRunError";
  }
}

/** Execution 表示 sandbox 中的一个后台执行。 */
export class Execution {
  constructor(
    private readonly transport: Transport,
    private readonly sandboxId: string,
    readonly id: string,
  ) {}

  /** 查询当前状态。 */
  async info(): Promise<ExecutionInfo> {
    const { value } = await this.transport.requestJSON<WireExecutionStatus>(
      "GET",
      `${sandboxBasePath(this.sandboxId)}/executions/${encodeURIComponent(this.id)}`,
    );
    return {
      executionId: value.execution_id,
      state: value.state,
      terminalEvent:
        value.terminal_event === undefined ? undefined : wireToExecutionEvent(value.terminal_event),
    };
  }

  /** 等待任一合法终态。 */
  async wait(timeoutMs = 90_000): Promise<ExecutionInfo> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const info = await this.info();
      if (isTerminalExecutionState(info.state)) {
        return info;
      }
      if (Date.now() > deadline) {
        throw new Error(`timeout waiting for execution ${this.id} terminal state`);
      }
      await sleep(this.transport.pollIntervalMs);
    }
  }

  /** 取消并等待终态。 */
  async cancelAndWait(timeoutMs = 90_000): Promise<ExecutionInfo> {
    await this.transport.requestJSON<unknown>(
      "DELETE",
      `${sandboxBasePath(this.sandboxId)}/executions/${encodeURIComponent(this.id)}`,
    );
    return this.wait(timeoutMs);
  }

  /** 读取一页日志。 */
  async logPage(cursor: number): Promise<ExecutionLogPage> {
    const { value } = await this.transport.requestJSON<WireLogPage>(
      "GET",
      `${sandboxBasePath(this.sandboxId)}/executions/${encodeURIComponent(this.id)}/logs?cursor=${cursor}`,
    );
    return {
      events: value.events.map(wireToExecutionEvent),
      nextCursor: value.next_cursor,
      complete: value.complete,
    };
  }

  /** 读取完整日志并解码 stdout/stderr。 */
  async collectLogs(timeoutMs = 90_000): Promise<{ stdout: Uint8Array; stderr: Uint8Array }> {
    const deadline = Date.now() + timeoutMs;
    let cursor = 0;
    const stdout: number[] = [];
    const stderr: number[] = [];
    for (;;) {
      const page = await this.logPage(cursor);
      for (const event of page.events) {
        if (event.type === "stdout" || event.type === "stderr") {
          const bytes = Buffer.from(event.dataBase64 ?? "", "base64");
          const target = event.type === "stdout" ? stdout : stderr;
          for (const byte of bytes) {
            target.push(byte);
          }
        }
      }
      cursor = page.nextCursor;
      if (page.complete) {
        return { stdout: Uint8Array.from(stdout), stderr: Uint8Array.from(stderr) };
      }
      if (Date.now() > deadline) {
        throw new Error(`timeout reading execution ${this.id} logs`);
      }
      await sleep(this.transport.pollIntervalMs);
    }
  }
}

function wireToExecutionEvent(value: WireExecutionEvent): ExecutionEvent {
  return {
    executionId: value.execution_id,
    sequence: value.sequence,
    timestamp: value.timestamp,
    type: value.type as ExecutionEvent["type"],
    dataBase64: value.data_base64,
    exitCode: value.exit_code,
    durationMs: value.duration_ms,
    outputTruncated: value.output_truncated,
    errorCode: value.error_code,
    message: value.message,
  };
}

function wireToSandboxInfo(value: WireSandbox): SandboxInfo {
  return {
    id: value.id,
    state: value.state,
    reason: value.reason,
    message: value.message,
    image: value.image,
    expiresAt: value.expires_at,
    createdAt: value.created_at,
    updatedAt: value.updated_at,
  };
}

function toWireExecuteRequest(request: ExecuteRequest, background: boolean): Record<string, unknown> {
  const wire: Record<string, unknown> = { background };
  if (request.argv !== undefined) {
    wire.argv = request.argv;
  }
  if (request.shell !== undefined) {
    wire.shell = request.shell;
  }
  if (request.cwd !== undefined) {
    wire.cwd = request.cwd;
  }
  if (request.env !== undefined) {
    wire.env = request.env;
  }
  if (request.timeoutSeconds !== undefined) {
    wire.timeout_seconds = request.timeoutSeconds;
  }
  return wire;
}
