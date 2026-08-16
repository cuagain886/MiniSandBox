"""MiniSandbox Python SDK 公共错误模型。"""


class ResponseError(Exception):
    """服务端返回了符合公共协议的非成功 HTTP 响应。"""

    def __init__(self, status, code, message, request_id, retryable):
        super().__init__(f"minisandbox: HTTP status {status}: {code}: {message}")
        self.status = status
        self.code = code
        self.request_id = request_id
        self.retryable = retryable

    def is_not_found(self):
        return self.status == 404

    def is_conflict(self):
        return self.status == 409


class RunError(Exception):
    """命令以非成功终态结束；result 携带已收集的输出。"""

    def __init__(self, result, detail):
        super().__init__(detail)
        self.result = result


class PTYError(Exception):
    """PTY 会话层错误。"""
