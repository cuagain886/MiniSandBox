import test from "node:test";
import assert from "node:assert/strict";
import { Client, ResponseError } from "../dist/index.js";

// 用可编程 stub fetch 驱动 SDK 基础闭环：创建、等待、执行、删除。
test("client lifecycle and execution facades", async (t) => {
  let getSandboxCalls = 0;
  const routes = new Map([
    ["POST /v1/sandboxes", () => jsonResponse(202, {
      id: "sbx-1", state: "Pending", reason: "CREATE_ACCEPTED", message: "ok",
      image: "debian:bookworm-slim", expires_at: "2026-08-16T00:00:00Z",
      created_at: "2026-08-15T23:00:00Z", updated_at: "2026-08-15T23:00:00Z",
    })],
    ["GET /v1/sandboxes/sbx-1", () => {
      getSandboxCalls += 1;
      return jsonResponse(200, {
        id: "sbx-1",
        state: getSandboxCalls >= 2 ? "Running" : "Creating",
        reason: "RUNNING", message: "ok", image: "debian:bookworm-slim",
        expires_at: "2026-08-16T00:00:00Z",
        created_at: "2026-08-15T23:00:00Z", updated_at: "2026-08-15T23:00:00Z",
      });
    }],
    ["POST /v1/sandboxes/sbx-1/executions", () => jsonResponse(202, { execution_id: "exec-1", state: "Pending" })],
    ["GET /v1/sandboxes/sbx-1/executions/exec-1", () => jsonResponse(200, {
      execution_id: "exec-1", state: "Exited",
      terminal_event: {
        execution_id: "exec-1", sequence: 2, timestamp: "2026-08-15T23:00:02Z",
        type: "exited", exit_code: 0, duration_ms: 42, output_truncated: false,
      },
    })],
    ["GET /v1/sandboxes/sbx-1/executions/exec-1/logs?cursor=0", () => jsonResponse(200, {
      events: [
        {
          execution_id: "exec-1", sequence: 1, timestamp: "2026-08-15T23:00:01Z",
          type: "stdout", data_base64: Buffer.from("ts-out").toString("base64"),
        },
        {
          execution_id: "exec-1", sequence: 2, timestamp: "2026-08-15T23:00:02Z",
          type: "exited", exit_code: 0, duration_ms: 42, output_truncated: false,
        },
      ],
      next_cursor: 2, complete: true,
    })],
    ["DELETE /v1/sandboxes/sbx-1", () => new Response(null, { status: 202 })],
  ]);

  const fetchImpl = makeStubFetch(routes);
  const client = new Client("http://127.0.0.1:8080", { fetchImpl, pollIntervalMs: 1 });

  const sandbox = await client.create({ image: "debian:bookworm-slim" }, { idempotencyKey: "ts-1" });
  assert.equal(sandbox.id, "sbx-1");
  const info = await sandbox.waitRunning();
  assert.equal(info.state, "Running");
  const result = await sandbox.run({ argv: ["/bin/true"] });
  assert.equal(result.exitCode, 0);
  assert.equal(Buffer.from(result.stdout).toString(), "ts-out");
  await sandbox.delete();
});

test("response error mapping", async () => {
  const fetchImpl = makeStubFetch(new Map([
    ["GET /v1/sandboxes/missing", () => jsonResponse(404, {
      error: { code: "SANDBOX_NOT_FOUND", message: "Sandbox does not exist.", request_id: "req-1", retryable: false },
    })],
  ]));
  const client = new Client("http://127.0.0.1:8080", { fetchImpl });
  await assert.rejects(
    () => client.sandbox("missing").info(),
    (error) => {
      assert.ok(error instanceof ResponseError);
      assert.equal(error.status, 404);
      assert.equal(error.code, "SANDBOX_NOT_FOUND");
      assert.ok(error.isNotFound());
      return true;
    },
  );
});

function jsonResponse(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeStubFetch(routes) {
  return (async (input, init) => {
    const url = new URL(String(input));
    const method = (init?.method ?? "GET").toUpperCase();
    const handler = routes.get(`${method} ${url.pathname}${url.search}`);
    if (!handler) {
      return jsonResponse(404, { error: { code: "NOT_FOUND", message: "no stub", request_id: "", retryable: false } });
    }
    return handler();
  });
}
