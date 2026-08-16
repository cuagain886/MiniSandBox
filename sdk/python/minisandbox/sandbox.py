"""Sandbox 与 Execution 资源对象：生命周期与命令执行。"""
import base64
import time
import urllib.parse

from .errors import RunError
from .files import SandboxFiles
from .portproxy import PortHTTP
from .pty import PTYConnection
from .transport import Transport, sleep

TERMINAL_EXECUTION_STATES = ("Exited", "Failed", "Cancelled", "TimedOut")


class Client:
    """MiniSandbox 生命周期 API 的同步 Python 客户端。"""

    def __init__(self, base_url, poll_interval=0.25, timeout=60):
        self._transport = Transport(base_url, poll_interval=poll_interval, timeout=timeout)

    def create(self, image, ttl_seconds=None, outbound=None, idempotency_key=None):
        """创建 sandbox 并返回资源对象。"""
        body = {"image": image}
        if ttl_seconds is not None:
            body["ttl_seconds"] = ttl_seconds
        if outbound is not None:
            body["network"] = {"outbound": outbound}
        headers = {}
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        _, sandbox = self._transport.request_json("POST", "/v1/sandboxes", body, headers)
        return Sandbox(self._transport, sandbox["id"])

    def sandbox(self, sandbox_id):
        """用已知 ID 绑定资源对象；不发起请求。"""
        return Sandbox(self._transport, sandbox_id)

    def health(self):
        """查询控制面存活状态。"""
        self._transport.request_json("GET", "/healthz")

    def readiness(self):
        """查询控制面与必要组件的就绪状态；未就绪不视为错误。"""
        try:
            _, value = self._transport.request_json("GET", "/readyz")
        except Exception:
            raise
        return {
            "ready": value.get("status") == "ready",
            "components": [
                {"name": component["name"], "ready": component["status"] == "ready"}
                for component in value.get("components", [])
            ],
        }


class Sandbox:
    """一个 sandbox 资源；本身不缓存状态。"""

    def __init__(self, transport, sandbox_id):
        self._transport = transport
        self.id = sandbox_id

    def _base(self):
        return "/v1/sandboxes/" + urllib.parse.quote(self.id)

    def info(self):
        """查询当前生命周期状态。"""
        _, value = self._transport.request_json("GET", self._base())
        return _map_sandbox(value)

    def wait_running(self, timeout=90):
        """轮询至 Running；Failed 或提前 Terminated 时抛错。"""
        deadline = time.monotonic() + timeout
        while True:
            info = self.info()
            if info["state"] == "Running":
                return info
            if info["state"] in ("Failed", "Terminated"):
                raise RuntimeError(
                    f"sandbox {self.id} entered {info['state']}: {info['reason']}: {info['message']}")
            if time.monotonic() > deadline:
                raise TimeoutError(f"timeout waiting for sandbox {self.id} to become Running")
            sleep(self._transport.poll_interval)

    def capabilities(self):
        """查询当前 sandbox 的功能能力。"""
        _, value = self._transport.request_json("GET", self._base() + "/capabilities")
        return {"files": value["files"], "pty": value["pty"], "http_port_proxy": value["http_port_proxy"]}

    def wait_ready(self, timeout=90):
        """等待 Running 并确认能力可用。"""
        info = self.wait_running(timeout)
        deadline = time.monotonic() + timeout
        while True:
            try:
                return info, self.capabilities()
            except Exception:
                pass
            if time.monotonic() > deadline:
                raise TimeoutError(f"timeout waiting for sandbox {self.id} capabilities")
            sleep(self._transport.poll_interval)

    def renew(self, expires_at):
        """延长租约到绝对时间。"""
        _, value = self._transport.request_json(
            "POST", self._base() + "/renew", {"expires_at": expires_at.isoformat()})
        return _map_sandbox(value)

    def delete(self):
        """提交删除意图并立即返回。"""
        self._transport.request_json("DELETE", self._base())

    def delete_and_wait(self, timeout=90):
        """删除并等待 Terminated。"""
        self.delete()
        deadline = time.monotonic() + timeout
        while True:
            info = self.info()
            if info["state"] == "Terminated":
                return info
            if info["state"] == "Failed":
                raise RuntimeError(f"sandbox {self.id} failed during deletion: {info['reason']}")
            if time.monotonic() > deadline:
                raise TimeoutError(f"timeout waiting for sandbox {self.id} to terminate")
            sleep(self._transport.poll_interval)

    def start_execution(self, argv=None, shell=None, cwd=None, env=None, timeout_seconds=None):
        """启动后台 execution 并返回资源对象。"""
        body = {"background": True}
        if argv is not None:
            body["argv"] = argv
        if shell is not None:
            body["shell"] = shell
        if cwd is not None:
            body["cwd"] = cwd
        if env is not None:
            body["env"] = env
        if timeout_seconds is not None:
            body["timeout_seconds"] = timeout_seconds
        _, value = self._transport.request_json("POST", self._base() + "/executions", body)
        return Execution(self._transport, self.id, value["execution_id"])

    def run(self, argv=None, shell=None, cwd=None, env=None, timeout_seconds=None, wait_timeout=90):
        """一次调用完成执行并收集输出；非成功终态抛 RunError。"""
        execution = self.start_execution(argv=argv, shell=shell, cwd=cwd, env=env,
                                         timeout_seconds=timeout_seconds)
        info = execution.wait(wait_timeout)
        stdout, stderr = execution.collect_logs(wait_timeout)
        terminal = info.get("terminal_event") or {}
        result = {
            "execution_id": execution.id,
            "state": info["state"],
            "exit_code": terminal.get("exit_code", -1),
            "stdout": stdout,
            "stderr": stderr,
            "duration_ms": terminal.get("duration_ms", 0),
            "output_truncated": terminal.get("output_truncated", False),
        }
        if info["state"] != "Exited" or result["exit_code"] != 0:
            raise RunError(result, f"execution {execution.id} {info['state']} "
                                   f"(exit {result['exit_code']})")
        return result

    def files(self):
        """返回 workspace 文件管理对象。"""
        return SandboxFiles(self._transport, self.id)

    def open_pty(self, argv, cols=80, rows=24, cwd=None, env=None, timeout_seconds=None):
        """打开一个交互式 PTY 会话。"""
        return PTYConnection.open(self._transport, self.id, argv, cols, rows,
                                  cwd=cwd, env=env, timeout_seconds=timeout_seconds)

    def port_http(self):
        """返回 sandbox loopback HTTP 代理对象。"""
        return PortHTTP(self._transport, self.id)


class Execution:
    """sandbox 中的一个后台执行。"""

    def __init__(self, transport, sandbox_id, execution_id):
        self._transport = transport
        self._sandbox_id = sandbox_id
        self.id = execution_id

    def _base(self):
        return ("/v1/sandboxes/" + urllib.parse.quote(self._sandbox_id) +
                "/executions/" + urllib.parse.quote(self.id))

    def info(self):
        """查询当前状态。"""
        _, value = self._transport.request_json("GET", self._base())
        return {
            "execution_id": value["execution_id"],
            "state": value["state"],
            "terminal_event": value.get("terminal_event"),
        }

    def wait(self, timeout=90):
        """等待任一合法终态。"""
        deadline = time.monotonic() + timeout
        while True:
            info = self.info()
            if info["state"] in TERMINAL_EXECUTION_STATES:
                return info
            if time.monotonic() > deadline:
                raise TimeoutError(f"timeout waiting for execution {self.id} terminal state")
            sleep(self._transport.poll_interval)

    def cancel_and_wait(self, timeout=90):
        """取消并等待终态。"""
        self._transport.request_json("DELETE", self._base())
        return self.wait(timeout)

    def log_page(self, cursor):
        """读取一页日志。"""
        _, value = self._transport.request_json("GET", f"{self._base()}/logs?cursor={cursor}")
        return value

    def collect_logs(self, timeout=90):
        """读取完整日志并解码 stdout/stderr。"""
        deadline = time.monotonic() + timeout
        cursor = 0
        stdout = bytearray()
        stderr = bytearray()
        while True:
            page = self.log_page(cursor)
            for event in page.get("events", []):
                if event["type"] in ("stdout", "stderr"):
                    data = base64.b64decode(event.get("data_base64", ""))
                    if event["type"] == "stdout":
                        stdout.extend(data)
                    else:
                        stderr.extend(data)
            cursor = page["next_cursor"]
            if page.get("complete"):
                return bytes(stdout), bytes(stderr)
            if time.monotonic() > deadline:
                raise TimeoutError(f"timeout reading execution {self.id} logs")
            sleep(self._transport.poll_interval)


def _map_sandbox(value):
    return {
        "id": value["id"],
        "state": value["state"],
        "reason": value["reason"],
        "message": value["message"],
        "image": value["image"],
        "expires_at": value["expires_at"],
        "created_at": value["created_at"],
        "updated_at": value["updated_at"],
    }
