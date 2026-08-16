"""sandbox loopback HTTP 代理：目标固定为 sandbox 内 127.0.0.1:port。"""
import urllib.parse


class PortHTTP:
    """受控的 sandbox 内 HTTP 服务访问。"""

    def __init__(self, transport, sandbox_id):
        self._transport = transport
        self._sandbox_id = sandbox_id

    def request(self, port, method, path, headers=None, body=None):
        """转发一次 HTTP 请求并返回 (status, headers, body_bytes)。

        上游业务状态原样透传；控制面错误抛 ResponseError。
        """
        target = ("/v1/sandboxes/" + urllib.parse.quote(self._sandbox_id) +
                  f"/ports/{port}/http" + path)
        status, response_headers, payload = self._transport.request_bytes(
            method, target, data=body, headers=headers)
        if "X-MiniSandbox-Proxied" not in response_headers:
            from .errors import ResponseError
            raise ResponseError(status, "PORT_PROXY_UNAVAILABLE",
                                "port proxy infrastructure error", "", True)
        return status, response_headers, payload
