"""用可编程 stub transport 驱动 Python SDK 基础闭环。"""
import base64
import json
import unittest
from unittest import mock

from minisandbox import Client, ResponseError, RunError


class StubTransport:
    def __init__(self, routes):
        self._routes = routes
        self.poll_interval = 0
        self.base_url = "http://127.0.0.1:8080"

    def request_json(self, method, path, body=None, headers=None):
        key = f"{method} {path}"
        handler = self._routes.get(key)
        if handler is None:
            raise AssertionError(f"unexpected request: {key}")
        return handler(self, body)


def make_client(routes):
    client = Client("http://127.0.0.1:8080")
    client._transport = StubTransport(routes)
    return client


class SDKTest(unittest.TestCase):
    def test_lifecycle_and_run(self):
        calls = {"gets": 0}

        def get_sandbox(_transport, _body):
            calls["gets"] += 1
            state = "Running" if calls["gets"] >= 2 else "Creating"
            return 200, {"id": "sbx-1", "state": state, "reason": "RUNNING", "message": "ok",
                         "image": "debian:bookworm-slim", "expires_at": "2026-08-16T00:00:00Z",
                         "created_at": "2026-08-15T23:00:00Z", "updated_at": "2026-08-15T23:00:00Z"}

        routes = {
            "POST /v1/sandboxes": lambda _t, body: (202, {"id": "sbx-1"}),
            "GET /v1/sandboxes/sbx-1": get_sandbox,
            "POST /v1/sandboxes/sbx-1/executions": lambda _t, body: (202, {"execution_id": "exec-1"}),
            "GET /v1/sandboxes/sbx-1/executions/exec-1": lambda _t, body: (200, {
                "execution_id": "exec-1", "state": "Exited",
                "terminal_event": {"execution_id": "exec-1", "sequence": 2,
                                   "timestamp": "2026-08-15T23:00:02Z", "type": "exited",
                                   "exit_code": 0, "duration_ms": 42,
                                   "output_truncated": False}}),
            "GET /v1/sandboxes/sbx-1/executions/exec-1/logs?cursor=0": lambda _t, body: (200, {
                "events": [
                    {"execution_id": "exec-1", "sequence": 1,
                     "timestamp": "2026-08-15T23:00:01Z", "type": "stdout",
                     "data_base64": base64.b64encode(b"py-out").decode("ascii")},
                    {"execution_id": "exec-1", "sequence": 2,
                     "timestamp": "2026-08-15T23:00:02Z", "type": "exited",
                     "exit_code": 0, "duration_ms": 42, "output_truncated": False},
                ],
                "next_cursor": 2, "complete": True}),
            "DELETE /v1/sandboxes/sbx-1": lambda _t, body: (202, {}),
        }
        client = make_client(routes)

        sandbox = client.create("debian:bookworm-slim")
        self.assertEqual(sandbox.id, "sbx-1")
        info = sandbox.wait_running()
        self.assertEqual(info["state"], "Running")
        result = sandbox.run(argv=["/bin/true"])
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual(result["stdout"], b"py-out")
        sandbox.delete()

    def test_run_error_carries_output(self):
        routes = {
            "POST /v1/sandboxes/sbx-1/executions": lambda _t, body: (202, {"execution_id": "exec-2"}),
            "GET /v1/sandboxes/sbx-1/executions/exec-2": lambda _t, body: (200, {
                "execution_id": "exec-2", "state": "Exited",
                "terminal_event": {"execution_id": "exec-2", "sequence": 2,
                                   "timestamp": "2026-08-15T23:00:02Z", "type": "exited",
                                   "exit_code": 7, "duration_ms": 5, "output_truncated": False}}),
            "GET /v1/sandboxes/sbx-1/executions/exec-2/logs?cursor=0": lambda _t, body: (200, {
                "events": [
                    {"execution_id": "exec-2", "sequence": 1,
                     "timestamp": "2026-08-15T23:00:01Z", "type": "stderr",
                     "data_base64": base64.b64encode(b"boom").decode("ascii")},
                    {"execution_id": "exec-2", "sequence": 2,
                     "timestamp": "2026-08-15T23:00:02Z", "type": "exited",
                     "exit_code": 7, "duration_ms": 5, "output_truncated": False},
                ],
                "next_cursor": 2, "complete": True}),
        }
        client = make_client(routes)
        sandbox = client.sandbox("sbx-1")
        with self.assertRaises(RunError) as caught:
            sandbox.run(argv=["/bin/false"])
        self.assertEqual(caught.exception.result["exit_code"], 7)
        self.assertEqual(caught.exception.result["stderr"], b"boom")


if __name__ == "__main__":
    unittest.main()
