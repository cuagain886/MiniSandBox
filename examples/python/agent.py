"""Python SDK 的完整 Agent 工作流示例。

运行方式：在仓库根目录执行 python3 examples/python/agent.py。
"""
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdk", "python"))

from minisandbox import Client  # noqa: E402


def main():
    client = Client("http://127.0.0.1:8080")

    sandbox = client.create("debian:bookworm-slim")
    try:
        _, capabilities = sandbox.wait_ready()
        print("capabilities:", capabilities)

        source = b"#!/bin/sh\necho py-build-ok > artifact.txt\n"
        sandbox.files().upload("src/build.sh", source, create_parents=True)

        result = sandbox.run(argv=["/bin/sh", "/workspace/src/build.sh"], timeout_seconds=30)
        print("run exit:", result["exit_code"])

        artifact = sandbox.files().download("artifact.txt")
        print("artifact:", artifact.decode("utf-8").strip())
    finally:
        try:
            sandbox.delete()
        except Exception:
            pass


if __name__ == "__main__":
    main()
