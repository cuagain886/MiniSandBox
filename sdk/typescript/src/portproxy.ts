/**
 * sandbox loopback HTTP 代理：目标固定为 sandbox 内 127.0.0.1:port。
 */
import { sandboxBasePath, Transport } from "./transport.js";

export interface PortHTTPRequest {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: Uint8Array | string;
}

/** PortHTTP 提供受控的 sandbox 内 HTTP 服务访问。 */
export class PortHTTP {
  constructor(
    private readonly transport: Transport,
    private readonly sandboxId: string,
  ) {}

  /** 转发一次 HTTP 请求并返回上游响应文本；上游状态原样透传。 */
  async request(port: number, request: PortHTTPRequest): Promise<{ status: number; headers: Record<string, string>; body: Uint8Array }> {
    const target = `${sandboxBasePath(this.sandboxId)}/ports/${port}/http${request.path}`;
    const body: BodyInit | undefined =
      request.body instanceof Uint8Array ? new Blob([new Uint8Array(request.body)]) : request.body;
    const response = await this.transport.fetch(target, {
      method: request.method,
      headers: request.headers,
      body,
    });
    if (response.headers.get("X-MiniSandbox-Proxied") === null) {
      throw await this.transport.decodeError(response);
    }
    const headers: Record<string, string> = {};
    response.headers.forEach((value, name) => {
      headers[name] = value;
    });
    return {
      status: response.status,
      headers,
      body: new Uint8Array(await response.arrayBuffer()),
    };
  }
}
