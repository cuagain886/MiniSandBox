/**
 * 交互式 PTY：一条 WebSocket 连接对应一个会话。
 *
 * 仅支持 Node.js 22+ 的原生 WebSocket；帧语义与公共协议一致。
 */
import { sandboxBasePath, Transport } from "./transport.js";
import { PTYStartRequest, PTYTerminalEvent } from "./wire.js";

/** PTYConnection 是一个已启动的交互式终端会话。 */
export class PTYConnection {
  private readonly socket: WebSocket;
  private readonly pending: Array<{ resolve: (chunk: Uint8Array) => void; reject: (error: Error) => void }> = [];
  private readonly queue: Uint8Array[] = [];
  private terminalEvent?: PTYTerminalEvent;
  private terminalWaiters: Array<{
    resolve: (event: PTYTerminalEvent) => void;
    reject: (error: Error) => void;
  }> = [];
  private failure?: Error;

  private constructor(socket: WebSocket) {
    this.socket = socket;
    socket.binaryType = "arraybuffer";
    socket.onmessage = (event) => this.handleMessage(event.data);
    socket.onclose = () => this.handleClose();
    socket.onerror = () => {
      this.failure ??= new Error("minisandbox: PTY connection error");
      this.drainPending();
    };
  }

  /** 打开一个 PTY 会话并等待 started 事件。 */
  static open(transport: Transport, sandboxId: string, request: PTYStartRequest): Promise<PTYConnection> {
    return new Promise((resolve, reject) => {
      const url = `${transport.baseUrl.replace(/^http/, "ws")}${sandboxBasePath(sandboxId)}/pty`;
      const socket = new WebSocket(url, "minisandbox.pty.v1");
      socket.binaryType = "arraybuffer";
      const fail = (message: string) => {
        reject(new Error(message));
        try {
          socket.close();
        } catch {
          // 关闭失败时保持静默；promise 已经拒绝。
        }
      };
      socket.onopen = () => {
        socket.send(
          JSON.stringify({
            type: "start",
            argv: request.argv,
            ...(request.cwd !== undefined ? { cwd: request.cwd } : {}),
            ...(request.env !== undefined ? { env: request.env } : {}),
            cols: request.cols,
            rows: request.rows,
            ...(request.timeoutSeconds !== undefined ? { timeout_seconds: request.timeoutSeconds } : {}),
          }),
        );
      };
      socket.onerror = () => fail("minisandbox: PTY connection failed");
      socket.onclose = () => fail("minisandbox: PTY connection closed before start");
      socket.onmessage = (event) => {
        const connection = new PTYConnection(socket);
        socket.onmessage = (message) => connection.handleMessage(message.data);
        socket.onclose = () => connection.handleClose();
        socket.onerror = () => connection.handleError();
        const payload = typeof event.data === "string" ? event.data : "";
        let parsed: { type?: string };
        try {
          parsed = JSON.parse(payload);
        } catch {
          fail("minisandbox: PTY started event is invalid");
          return;
        }
        if (parsed.type !== "started") {
          fail(`minisandbox: PTY first message must be started, got ${parsed.type}`);
          return;
        }
        resolve(connection);
      };
    });
  }

  /** 向终端 stdin 写入字节。 */
  write(chunk: Uint8Array | string): void {
    const payload = typeof chunk === "string" ? chunk : chunk.buffer;
    this.socket.send(payload as BufferSource);
  }

  /** 调整终端窗口大小。 */
  resize(cols: number, rows: number): void {
    this.socket.send(JSON.stringify({ type: "resize", cols, rows }));
  }

  /** 关闭会话；等价于取消。 */
  close(): void {
    this.socket.close();
  }

  /** 逐块读取终端输出；PTY 天生合并 stdout 与 stderr。 */
  readOutput(): Promise<Uint8Array> {
    const buffered = this.queue.shift();
    if (buffered !== undefined) {
      return Promise.resolve(buffered);
    }
    if (this.failure !== undefined) {
      return Promise.reject(this.failure);
    }
    if (this.terminalEvent !== undefined || this.socket.readyState === WebSocket.CLOSED) {
      return Promise.reject(new Error("minisandbox: PTY output ended"));
    }
    return new Promise((resolve, reject) => {
      this.pending.push({ resolve, reject });
    });
  }

  /** 等待唯一终态事件。 */
  waitTerminal(): Promise<PTYTerminalEvent> {
    if (this.terminalEvent !== undefined) {
      return Promise.resolve(this.terminalEvent);
    }
    if (this.failure !== undefined) {
      return Promise.reject(this.failure);
    }
    return new Promise((resolve, reject) => {
      this.terminalWaiters.push({ resolve, reject });
    });
  }

  private handleMessage(data: unknown): void {
    if (typeof data === "string") {
      let parsed: { type?: string; exit_code?: number; duration_ms?: number; error_code?: string; message?: string };
      try {
        parsed = JSON.parse(data);
      } catch {
        return;
      }
      if (parsed.type === "terminal") {
        this.terminalEvent = {
          type: "terminal",
          exitCode: parsed.exit_code,
          durationMs: parsed.duration_ms,
          errorCode: parsed.error_code,
          message: parsed.message,
        };
        for (const waiter of this.terminalWaiters.splice(0)) {
          waiter.resolve(this.terminalEvent);
        }
        this.drainPending();
        this.close();
      }
      return;
    }
    const chunk = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array(0);
    const waiter = this.pending.shift();
    if (waiter !== undefined) {
      waiter.resolve(chunk);
      return;
    }
    this.queue.push(chunk);
  }

  private handleClose(): void {
    if (this.terminalEvent === undefined) {
      this.failure ??= new Error("minisandbox: PTY connection closed");
    }
    this.drainPending();
    this.drainTerminalWaiters();
  }

  private handleError(): void {
    this.failure ??= new Error("minisandbox: PTY connection error");
    this.drainPending();
    this.drainTerminalWaiters();
  }

  private drainPending(): void {
    for (const waiter of this.pending.splice(0)) {
      waiter.reject(this.failure ?? new Error("minisandbox: PTY output ended"));
    }
  }

  private drainTerminalWaiters(): void {
    if (this.failure === undefined) {
      return;
    }
    for (const waiter of this.terminalWaiters.splice(0)) {
      waiter.reject(this.failure);
    }
  }
}

/** 打开一个交互式 PTY 会话。 */
export function openPTY(transport: Transport, sandboxId: string, request: PTYStartRequest): Promise<PTYConnection> {
  return PTYConnection.open(transport, sandboxId, request);
}
