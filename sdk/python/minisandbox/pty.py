"""交互式 PTY：最小 RFC 6455 客户端实现。

仅连接本服务的 minisandbox.pty.v1 子协议，不作为通用 WebSocket 库；
客户端帧按规范强制掩码。
"""
import base64
import json
import os
import socket
import struct
import urllib.parse

from .errors import PTYError

SUBPROTOCOL = "minisandbox.pty.v1"

_OPCODE_TEXT = 0x1
_OPCODE_BINARY = 0x2
_OPCODE_CLOSE = 0x8


class PTYConnection:
    """一个已启动的交互式终端会话。

    write 与 resize 可重复调用；read_output 逐块返回合并的终端输出；
    wait_terminal 阻塞等待唯一终态事件；close 等价于取消。
    """

    @classmethod
    def open(cls, transport, sandbox_id, argv, cols, rows, cwd=None, env=None,
             timeout_seconds=None):
        """建立连接、发送 start 并等待 started 事件。"""
        parsed = urllib.parse.urlparse(transport.base_url)
        host = parsed.hostname or "127.0.0.1"
        port = parsed.port or (443 if parsed.scheme == "https" else 80)
        path = ("/v1/sandboxes/" + urllib.parse.quote(sandbox_id) + "/pty")
        connection = cls(host, port, path)
        try:
            connection._handshake()
            start = {"type": "start", "argv": argv, "cols": cols, "rows": rows}
            if cwd:
                start["cwd"] = cwd
            if env:
                start["env"] = env
            if timeout_seconds:
                start["timeout_seconds"] = timeout_seconds
            connection._send_text(json.dumps(start))
            opcode, payload = connection._read_frame()
            if opcode != _OPCODE_TEXT:
                raise PTYError("minisandbox: PTY first message must be started")
            event = json.loads(payload.decode("utf-8"))
            if event.get("type") != "started":
                raise PTYError(f"minisandbox: PTY start rejected: {event.get('error_code', '')}")
            return connection
        except Exception:
            connection.close()
            raise

    def __init__(self, host, port, path):
        self._socket = None
        self._host = host
        self._port = port
        self._path = path
        self._terminal = None

    def _handshake(self):
        self._socket = socket.create_connection((self._host, self._port), timeout=60)
        self._socket.settimeout(60)
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        request = (
            f"GET {self._path} HTTP/1.1\r\n"
            f"Host: {self._host}:{self._port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            f"Sec-WebSocket-Version: 13\r\n"
            f"Sec-WebSocket-Protocol: {SUBPROTOCOL}\r\n\r\n"
        )
        self._socket.sendall(request.encode("ascii"))
        response = b""
        while b"\r\n\r\n" not in response:
            chunk = self._socket.recv(4096)
            if not chunk:
                raise PTYError("minisandbox: PTY handshake ended early")
            response += chunk
        header = response.split(b"\r\n\r\n", 1)[0].decode("latin-1")
        if " 101 " not in header.split("\r\n")[0]:
            raise PTYError(f"minisandbox: PTY handshake rejected: {header.splitlines()[0]}")
        if SUBPROTOCOL not in header:
            raise PTYError("minisandbox: PTY subprotocol mismatch")
        self._buffer = response.split(b"\r\n\r\n", 1)[1]

    def _send_frame(self, opcode, payload):
        if self._socket is None:
            raise PTYError("minisandbox: PTY connection is closed")
        header = bytes([0x80 | opcode])
        length = len(payload)
        mask = os.urandom(4)
        if length < 126:
            header += bytes([0x80 | length])
        elif length < (1 << 16):
            header += bytes([0x80 | 126]) + struct.pack(">H", length)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", length)
        header += mask
        masked = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
        self._socket.sendall(header + masked)

    def _send_text(self, text):
        self._send_frame(_OPCODE_TEXT, text.encode("utf-8"))

    def write(self, data):
        """向终端 stdin 写入字节或文本。"""
        payload = data.encode("utf-8") if isinstance(data, str) else data
        self._send_frame(_OPCODE_BINARY, payload)

    def resize(self, cols, rows):
        """调整终端窗口大小。"""
        self._send_text(json.dumps({"type": "resize", "cols": cols, "rows": rows}))

    def _read_exact(self, count):
        while len(self._buffer) < count:
            chunk = self._socket.recv(65536)
            if not chunk:
                raise PTYError("minisandbox: PTY connection closed")
            self._buffer += chunk
        result, self._buffer = self._buffer[:count], self._buffer[count:]
        return result

    def _read_frame(self):
        first, second = self._read_exact(2)
        opcode = first & 0x0F
        length = second & 0x7F
        if length == 126:
            length = struct.unpack(">H", self._read_exact(2))[0]
        elif length == 127:
            length = struct.unpack(">Q", self._read_exact(8))[0]
        payload = self._read_exact(length)
        return opcode, payload

    def read_output(self):
        """读取下一块终端输出；会话结束后抛 PTYError。"""
        while True:
            opcode, payload = self._read_frame()
            if opcode == _OPCODE_BINARY:
                return payload
            if opcode == _OPCODE_TEXT:
                event = json.loads(payload.decode("utf-8"))
                if event.get("type") == "terminal":
                    self._terminal = event
                    raise PTYError("minisandbox: PTY output ended")
            elif opcode == _OPCODE_CLOSE:
                raise PTYError("minisandbox: PTY output ended")

    def wait_terminal(self):
        """阻塞等待唯一终态事件并返回其内容。"""
        try:
            while self._terminal is None:
                opcode, payload = self._read_frame()
                if opcode == _OPCODE_TEXT:
                    event = json.loads(payload.decode("utf-8"))
                    if event.get("type") == "terminal":
                        self._terminal = event
                elif opcode == _OPCODE_CLOSE:
                    raise PTYError("minisandbox: PTY closed without terminal")
            return self._terminal
        finally:
            self.close()

    def close(self):
        """关闭会话；等价于取消。"""
        if self._socket is not None:
            try:
                self._send_frame(_OPCODE_CLOSE, b"")
            except OSError:
                pass
            finally:
                self._socket.close()
                self._socket = None
