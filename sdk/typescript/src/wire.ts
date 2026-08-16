/**
 * MiniSandbox TypeScript SDK 的公共 wire 模型。
 *
 * 字段与 OpenAPI 契约保持一致；时间使用 RFC3339 字符串，时长使用秒数。
 */

export type SandboxState = "Pending" | "Creating" | "Running" | "Stopping" | "Terminated" | "Failed";

export type ExecutionState = "Pending" | "Running" | "Exited" | "Failed" | "Cancelled" | "TimedOut";

export type EventType = "started" | "stdout" | "stderr" | "output_limit_reached" | "exited" | "failed" | "cancelled" | "timed_out";

export interface CreateSandboxRequest {
  image: string;
  ttlSeconds?: number;
  network?: { outbound: boolean };
}

export interface SandboxInfo {
  id: string;
  state: SandboxState;
  reason: string;
  message: string;
  image: string;
  expiresAt: string;
  createdAt: string;
  updatedAt: string;
}

export interface ExecuteRequest {
  argv?: string[];
  shell?: string;
  cwd?: string;
  env?: Record<string, string>;
  timeoutSeconds?: number;
}

export interface ExecutionDescriptor {
  executionId: string;
  state: ExecutionState;
}

export interface ExecutionEvent {
  executionId: string;
  sequence: number;
  timestamp: string;
  type: EventType;
  dataBase64?: string;
  exitCode?: number;
  durationMs?: number;
  outputTruncated?: boolean;
  errorCode?: string;
  message?: string;
}

export interface ExecutionInfo {
  executionId: string;
  state: ExecutionState;
  terminalEvent?: ExecutionEvent;
}

export interface ExecutionLogPage {
  events: ExecutionEvent[];
  nextCursor: number;
  complete: boolean;
}

export interface RunResult {
  executionId: string;
  state: ExecutionState;
  exitCode: number;
  stdout: Uint8Array;
  stderr: Uint8Array;
  durationMs: number;
  outputTruncated: boolean;
}

export interface Capabilities {
  files: boolean;
  pty: boolean;
  httpPortProxy: boolean;
}

export interface FileStat {
  path: string;
  type: "regular" | "directory" | "symlink" | "other";
  sizeBytes: number;
  mode: string;
  modifiedAt: string;
}

export interface Readiness {
  ready: boolean;
  components: Array<{ name: string; ready: boolean }>;
}

export interface PTYStartRequest {
  argv: string[];
  cwd?: string;
  env?: Record<string, string>;
  cols: number;
  rows: number;
  timeoutSeconds?: number;
}

export interface PTYTerminalEvent {
  type: "started" | "terminal" | "error";
  exitCode?: number;
  durationMs?: number;
  errorCode?: string;
  message?: string;
}

export const TERMINAL_EXECUTION_STATES: readonly ExecutionState[] = [
  "Exited",
  "Failed",
  "Cancelled",
  "TimedOut",
];

export function isTerminalExecutionState(state: ExecutionState): boolean {
  return TERMINAL_EXECUTION_STATES.includes(state);
}
