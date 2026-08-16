"""workspace 文件管理：路径为 workspace 相对路径，"." 表示根目录。"""
import json
import urllib.parse


class SandboxFiles:
    """workspace 文件的 SDK 易用接口。"""

    def __init__(self, transport, sandbox_id):
        self._transport = transport
        self._sandbox_id = sandbox_id

    def _base(self):
        return "/v1/sandboxes/" + urllib.parse.quote(self._sandbox_id)

    def stat(self, path):
        """查询一个路径的 metadata。"""
        _, value = self._transport.request_json("POST", self._base() + "/files/stat", {"path": path})
        return _map_stat(value)

    def list(self, path):
        """列出目录直接子项。"""
        _, value = self._transport.request_json(
            "POST", self._base() + "/directories/list", {"path": path})
        return [_map_stat(entry) for entry in value.get("entries", [])]

    def mkdir(self, path, parents=False):
        """创建目录；parents 为 True 时创建缺失祖先并接受已存在目录。"""
        _, value = self._transport.expect_json(
            "POST", self._base() + "/directories", {"path": path, "parents": parents}, (200, 201))
        return _map_stat(value)

    def upload(self, path, content, overwrite=False, create_parents=False):
        """把二进制内容上传到一个 workspace 文件；上传是原子的。"""
        query = urllib.parse.urlencode({
            "path": path, "overwrite": str(overwrite).lower(),
            "create_parents": str(create_parents).lower()})
        _, _, body = self._transport.request_bytes(
            "PUT", self._base() + "/files/content?" + query, data=content,
            headers={"Content-Type": "application/octet-stream", "Accept": "application/json"})
        return _map_stat(json.loads(body.decode("utf-8")))

    def download(self, path):
        """下载一个普通文件并返回完整字节。"""
        query = urllib.parse.urlencode({"path": path})
        _, _, body = self._transport.request_bytes(
            "GET", self._base() + "/files/content?" + query,
            headers={"Accept": "application/octet-stream"})
        return body

    def move(self, source, destination, overwrite=False):
        """在 workspace 内移动路径。"""
        _, value = self._transport.request_json(
            "POST", self._base() + "/files/move",
            {"source": source, "destination": destination, "overwrite": overwrite})
        return _map_stat(value)

    def delete(self, path, recursive=False):
        """删除文件或目录；目标不存在同样成功。"""
        self._transport.request_json(
            "POST", self._base() + "/files/delete", {"path": path, "recursive": recursive})


def _map_stat(value):
    return {
        "path": value["path"],
        "type": value["type"],
        "size_bytes": value["size_bytes"],
        "mode": value["mode"],
        "modified_at": value["modified_at"],
    }
