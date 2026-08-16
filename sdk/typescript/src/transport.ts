/**
 * HTTP 传输层：统一请求编码、状态检查和公共错误解码。
 */
import { ResponseError } from "./errors.js";

interface WireErrorDetail {
  code: string;
  message: string;
  request_id: string;
  retryable: boolean;
}

interface WireErrorResponse {
  error: WireErrorDetail;
}

export interface TransportOptions {
  baseUrl: string;
  fetchImpl?: typeof fetch;
  pollIntervalMs?: number;
}

export const DEFAULT_POLL_INTERVAL_MS = 250;

export class Transport {
  readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;
  readonly pollIntervalMs: number;

  constructor(options: TransportOptions) {
    this.baseUrl = options.baseUrl.replace(/\/$/, "");
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.pollIntervalMs = options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS;
  }

  async requestJSON<T>(
    method: string,
    path: string,
    body?: unknown,
    options: { headers?: Record<string, string>; accept?: string } = {},
  ): Promise<{ status: number; value: T }> {
    const response = await this.fetchImpl(this.baseUrl + path, {
      method,
      headers: {
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        ...(options.accept !== undefined ? { Accept: options.accept } : {}),
        ...options.headers,
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (!response.ok) {
      throw await decodeResponseError(response);
    }
    if (response.status === 204) {
      return { status: response.status, value: undefined as T };
    }
    return { status: response.status, value: (await response.json()) as T };
  }

  async expectJSON<T>(
    method: string,
    path: string,
    body: unknown,
    acceptedStatuses: number[],
    options: { headers?: Record<string, string> } = {},
  ): Promise<{ status: number; value: T }> {
    const response = await this.fetchImpl(this.baseUrl + path, {
      method,
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json",
        ...options.headers,
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (!acceptedStatuses.includes(response.status)) {
      throw await decodeResponseError(response);
    }
    if (response.status === 204) {
      return { status: response.status, value: undefined as T };
    }
    return { status: response.status, value: (await response.json()) as T };
  }
}

async function decodeResponseError(response: Response): Promise<ResponseError> {
  let detail: WireErrorDetail | undefined;
  try {
    const envelope = (await response.json()) as WireErrorResponse;
    detail = envelope?.error;
  } catch {
    detail = undefined;
  }
  if (!detail || typeof detail.code !== "string") {
    return new ResponseError(
      response.status,
      "INVALID_RESPONSE",
      `HTTP status ${response.status} with invalid error response`,
      "",
      false,
    );
  }
  return new ResponseError(response.status, detail.code, detail.message, detail.request_id, detail.retryable);
}

export function sandboxBasePath(sandboxId: string): string {
  return `/v1/sandboxes/${encodeURIComponent(sandboxId)}`;
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
