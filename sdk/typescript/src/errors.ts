/**
 * 公共错误模型：调用方可直接判断 HTTP 状态与稳定错误码。
 */
export class ResponseError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  readonly retryable: boolean;

  constructor(status: number, code: string, message: string, requestId: string, retryable: boolean) {
    super(message);
    this.name = "MiniSandboxResponseError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.retryable = retryable;
  }

  isNotFound(): boolean {
    return this.status === 404;
  }

  isConflict(): boolean {
    return this.status === 409;
  }
}
