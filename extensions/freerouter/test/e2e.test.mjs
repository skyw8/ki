// End-to-end sidecar test: spawn bin/extension.js exactly as ki would
// (NDJSON over stdio, KI_EXTENSION_ROOT pointing at a temp config), point its
// baseUrl at a local mock OpenRouter, and drive a full routed stream.

import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, copyFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";

const PKG_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function startMockOpenRouter() {
  const server = createServer((req, res) => {
    if (req.url === "/v1/models") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          data: [
            { id: "groq/a:free", name: "A", context_length: 32000 },
            { id: "cerebras/b:free", name: "B", context_length: 64000 },
          ],
        }),
      );
      return;
    }
    if (req.url === "/v1/chat/completions") {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const { model } = JSON.parse(body);
        if (model === "groq/a:free") {
          res.writeHead(429);
          res.end("rate limited");
          return;
        }
        res.writeHead(200, { "content-type": "text/event-stream" });
        res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "hello from b" } }] })}\n\n`);
        res.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 } })}\n\n`);
        res.write("data: [DONE]\n\n");
        res.end();
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server)));
}

function readLines(stream, onLine) {
  let buffer = "";
  stream.setEncoding("utf8");
  stream.on("data", (chunk) => {
    buffer += chunk;
    let idx;
    while ((idx = buffer.indexOf("\n")) !== -1) {
      const line = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 1);
      if (line.trim()) onLine(line);
    }
  });
}

test("sidecar end-to-end: initialize, routed stream, config.updated", async () => {
  const server = await startMockOpenRouter();
  const port = server.address().port;

  const root = mkdtempSync(join(tmpdir(), "ki-fr-e2e-"));
  copyFileSync(join(PKG_ROOT, "extension.json"), join(root, "extension.json"));
  writeFileSync(
    join(root, "config.json"),
    JSON.stringify({ baseUrl: `http://127.0.0.1:${port}/v1`, apiKey: "test-key", raceWidth: 2 }),
  );

  const child = spawn(process.execPath, [join(PKG_ROOT, "bin", "extension.js")], {
    env: { ...process.env, KI_EXTENSION: "freerouter", KI_EXTENSION_ROOT: root, KI_HOME: root },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const stderr = [];
  child.stderr.on("data", (c) => stderr.push(c.toString()));

  const responses = new Map();
  const notifications = [];
  const waiters = [];
  readLines(child.stdout, (line) => {
    const msg = JSON.parse(line);
    if (msg.id !== undefined) {
      responses.set(msg.id, msg);
      waiters.splice(0).forEach((w) => w());
    } else if (msg.method) {
      notifications.push(msg);
      waiters.splice(0).forEach((w) => w());
    }
  });
  const waitFor = (predicate) =>
    new Promise((resolve) => {
      const check = () => {
        if (predicate()) resolve();
        else waiters.push(check);
      };
      check();
    });

  const send = (msg) => child.stdin.write(JSON.stringify(msg) + "\n");
  const result = (id) => responses.get(id).result;

  try {
    send({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} });
    await waitFor(() => responses.has(1));
    assert.deepEqual(result(1), { tools: [], commands: [], fallback: false, subscriptions: [] });

    send({
      jsonrpc: "2.0",
      id: 2,
      method: "provider.stream.start",
      params: {
        requestId: "stream-1",
        request: {
          provider: "free-router",
          model: { id: "auto", api: "freerouter", provider: "free-router", contextWindow: 128000, maxTokens: 8192 },
          credential: { type: "api_key", apiKey: "test-key" },
          request: { system: "sys", messages: [{ role: "user", content: [{ type: "text", text: "hi" }] }], tools: [], maxTokens: 100 },
        },
      },
    });
    await waitFor(() => responses.has(2));
    assert.deepEqual(result(2), { accepted: true });

    await waitFor(
      () =>
        notifications.some((n) => n.method === "provider.stream.event" && n.params.type === "done") ||
        notifications.some((n) => n.method === "provider.stream.event" && n.params.type === "error"),
    );
    const streamEvents = notifications
      .filter((n) => n.method === "provider.stream.event" && n.params.requestId === "stream-1")
      .map((n) => n.params);
    assert.equal(streamEvents[0].type, "thinking_start");

    // groq/a:free answers 429, so cerebras/b:free must win the race.
    const done = streamEvents.find((e) => e.type === "done");
    assert.ok(done, `expected done, got: ${JSON.stringify(streamEvents)}`);
    assert.equal(done.message.model, "cerebras/b:free");
    assert.equal(done.message.content[1].text, "hello from b");
    assert.equal(done.message.usage.totalTokens, 7);
    assert.ok(done.message.content[0].thinking.includes("Using cerebras/b:free"));
    assert.ok(
      streamEvents.some((e) => e.type === "text_delta" && e.delta === "hello from b" && e.contentIndex === 1),
    );

    // config.updated is a notification; the sidecar must survive it.
    send({ jsonrpc: "2.0", method: "config.updated", params: { config: {} } });
    send({ jsonrpc: "2.0", id: 3, method: "initialize", params: {} });
    await waitFor(() => responses.has(3));
    assert.ok(responses.has(3));
  } finally {
    child.kill("SIGKILL");
    server.close();
  }
}, { timeout: 15000 });
