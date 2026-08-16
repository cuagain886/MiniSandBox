/**
 * workspace 文件管理：路径为 workspace 相对路径，"." 表示根目录。
 */
import { sandboxBasePath, Transport } from "./transport.js";
import { FileStat } from "./wire.js";

interface WireFileStat {
  path: string;
  type: FileStat["type"];
  size_bytes: number;
  mode: string;
  modified_at: string;
}

interface WireDirectoryListing {
  path: string;
  entries: WireFileStat[];
}

export interface UploadOptions {
  overwrite?: boolean;
  createParents?: boolean;
}

/** SandboxFiles 提供 workspace 文件的 SDK 易用接口。 */
export class SandboxFiles {
  constructor(
    private readonly transport: Transport,
    private readonly sandboxId: string,
  ) {}

  /** 查询一个路径的 metadata。 */
  async stat(path: string): Promise<FileStat> {
    const { value } = await this.transport.requestJSON<WireFileStat>(
      "POST",
      `${sandboxBasePath(this.sandboxId)}/files/stat`,
      { path },
    );
    return wireToFileStat(value);
  }

  /** 列出目录直接子项。 */
  async list(path: string): Promise<FileStat[]> {
    const { value } = await this.transport.requestJSON<WireDirectoryListing>(
      "POST",
      `${sandboxBasePath(this.sandboxId)}/directories/list`,
      { path },
    );
    return value.entries.map(wireToFileStat);
  }

  /** 创建目录；parents 为 true 时创建缺失祖先并接受已存在目录。 */
  async mkdir(path: string, parents: boolean): Promise<FileStat> {
    const { value } = await this.transport.expectJSON<WireFileStat>(
      "POST",
      `${sandboxBasePath(this.sandboxId)}/directories`,
      { path, parents },
      [200, 201],
    );
    return wireToFileStat(value);
  }

  /** 把二进制内容上传到一个 workspace 文件；上传是原子的。 */
  async upload(path: string, content: Uint8Array | Blob, options: UploadOptions = {}): Promise<FileStat> {
    const query = new URLSearchParams({
      path,
      overwrite: String(options.overwrite ?? false),
      create_parents: String(options.createParents ?? false),
    });
    const body =
      content instanceof Uint8Array ? new Blob([new Uint8Array(content)]) : content;
    const response = await this.transport.fetch(
      `${sandboxBasePath(this.sandboxId)}/files/content?${query.toString()}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/octet-stream", Accept: "application/json" },
        body,
      },
    );
    if (response.status !== 200 && response.status !== 201) {
      throw await this.transport.decodeError(response);
    }
    return wireToFileStat((await response.json()) as WireFileStat);
  }

  /** 流式下载一个普通文件并返回完整字节。 */
  async download(path: string): Promise<Uint8Array> {
    const query = new URLSearchParams({ path });
    const response = await this.transport.fetch(
      `${sandboxBasePath(this.sandboxId)}/files/content?${query.toString()}`,
      { method: "GET", headers: { Accept: "application/octet-stream" } },
    );
    if (!response.ok) {
      throw await this.transport.decodeError(response);
    }
    return new Uint8Array(await response.arrayBuffer());
  }

  /** 在 workspace 内移动路径。 */
  async move(source: string, destination: string, overwrite: boolean): Promise<FileStat> {
    const { value } = await this.transport.requestJSON<WireFileStat>(
      "POST",
      `${sandboxBasePath(this.sandboxId)}/files/move`,
      { source, destination, overwrite },
    );
    return wireToFileStat(value);
  }

  /** 删除文件或目录；目标不存在同样成功。 */
  async remove(path: string, recursive: boolean): Promise<void> {
    await this.transport.requestJSON<unknown>(
      "POST",
      `${sandboxBasePath(this.sandboxId)}/files/delete`,
      { path, recursive },
    );
  }
}

function wireToFileStat(value: WireFileStat): FileStat {
  return {
    path: value.path,
    type: value.type,
    sizeBytes: value.size_bytes,
    mode: value.mode,
    modifiedAt: value.modified_at,
  };
}
