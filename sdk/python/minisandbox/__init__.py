"""MiniSandbox Python SDK（同步、纯标准库）。

面向调用方的入口：Client → Sandbox → Execution / Files / PTY / PortHTTP。
"""
from .errors import PTYError, ResponseError, RunError
from .sandbox import Client, Execution, Sandbox

__all__ = ["Client", "Sandbox", "Execution", "ResponseError", "RunError", "PTYError"]
