/**
 * TypeScript SDK 的完整 Agent 工作流示例。
 *
 * 运行方式：先构建 sdk/typescript（npm install && npm run build），再执行
 * npx tsx examples/typescript/agent.ts（或编译后用 node 运行）。
 */
import { Client } from "../../sdk/typescript/dist/index.js";

async function main() {
  const client = new Client("http://127.0.0.1:8080");

  const sandbox = await client.create({ image: "debian:bookworm-slim" });
  try {
    const { capabilities } = await sandbox.waitReady();
    console.log("capabilities:", capabilities);

    const source = new TextEncoder().encode("#!/bin/sh\necho ts-build-ok > artifact.txt\n");
    await sandbox.files().upload("src/build.sh", source, { createParents: true });

    const result = await sandbox.run({
      argv: ["/bin/sh", "/workspace/src/build.sh"],
      timeoutSeconds: 30,
    });
    console.log("run exit:", result.exitCode);

    const artifact = await sandbox.files().download("artifact.txt");
    console.log("artifact:", new TextDecoder().decode(artifact));
  } finally {
    await sandbox.delete().catch(() => undefined);
  }
}

main().catch((error) => {
  console.error("agent workflow failed:", error);
  process.exit(1);
});
