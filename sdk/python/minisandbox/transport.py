"""HTTP 传输层：统一请求编码、状态检查和公共错误解码。"""
import json
import time
import urllib.error
import urllib.request

from .errors import ResponseError

DEFAULT_POLL_INTERVAL_SECONDS = 0.25


class Transport:
    """基于 urllib 的同步传输。"""

    def __init__(self, base_url, poll_interval=DEFAULT_POLL_INTERVAL_SECONDS, timeout=60):
        self.base_url = base_url.rstrip("/")
        self.poll_interval = poll_interval
        self.timeout = timeout

    def request_json(self, method, path, body=None, headers=None):
        """执行 JSON 请求并返回 (status, decoded)。非 2xx 抛 ResponseError。"""
        data = None
        request_headers = dict(headers or {})
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            request_headers.setdefault("Content-Type", "application/json")
        request = urllib.request.Request(self.base_url + path, data=data, method=method,
                                         headers=request_headers)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                status = response.status
                payload = response.read()
        except urllib.error.HTTPError as error:
            raise _decode_error(error) from None
        if status == 204 or not payload:
            return status, None
        return status, json.loads(payload.decode("utf-8"))

    def expect_json(self, method, path, body, accepted_statuses, headers=None):
        """执行 JSON 请求并在多个可接受状态码下解码。"""
        data = json.dumps(body).encode("utf-8")
        request_headers = {"Content-Type": "application/json", "Accept": "application/json"}
        request_headers.update(headers or {})
        request = urllib.request.Request(self.base_url + path, data=data, method=method,
                                         headers=request_headers)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                status = response.status
                payload = response.read()
        except urllib.error.HTTPError as error:
            if error.code in accepted_statuses:
                payload = error.read()
                return error.code, json.loads(payload.decode("utf-8")) if payload else None
            raise _decode_error(error) from None
        if status not in accepted_statuses:
            raise ResponseError(status, "UNEXPECTED_STATUS", "unexpected HTTP status", "", False)
        return status, json.loads(payload.decode("utf-8")) if payload else None

    def request_bytes(self, method, path, data=None, headers=None):
        """执行字节请求并返回 (status, headers, body)。"""
        request = urllib.request.Request(self.base_url + path, data=data, method=method,
                                         headers=headers or {})
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as error:
            raise _decode_error(error) from None


def _decode_error(error):
    try:
        envelope = json.loads(error.read().decode("utf-8"))
        detail = envelope.get("error", {})
        return ResponseError(error.code, detail.get("code", ""), detail.get("message", ""),
                             detail.get("request_id", ""), bool(detail.get("retryable")))
    except (ValueError, OSError):
        return ResponseError(error.code, "INVALID_RESPONSE",
                             f"HTTP status {error.code} with invalid error response", "", False)


def sleep(seconds):
    time.sleep(seconds)
