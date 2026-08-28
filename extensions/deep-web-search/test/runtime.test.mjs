import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const extensionPath = fileURLToPath(new URL("..", import.meta.url));
const runtimePath = join(extensionPath, "dist", "main.js");

function run(messages, extensionRoot) {
  const output = execFileSync(process.execPath, [runtimePath], {
    input: `${messages.map((message) => JSON.stringify(message)).join("\n")}\n`,
    cwd: extensionPath,
    env: { ...process.env, KI_HOME: extensionRoot, KI_EXTENSION_ROOT: extensionRoot },
    encoding: "utf8",
    timeout: 10_000,
  });
  return output.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
}

test("sidecar registers the public tools and rejects an empty query", async () => {
  const root = await mkdtemp(join(tmpdir(), "ki-deep-web-search-"));
  const output = run([
    { jsonrpc: "2.0", id: 1, method: "initialize", params: {} },
    { jsonrpc: "2.0", id: 2, method: "tool.execute", params: { sessionId: "s1", name: "deep_web_search", args: {} } },
    { jsonrpc: "2.0", id: 3, method: "shutdown", params: {} },
  ], root);
  const byId = new Map(output.map((item) => [String(item.id), item]));
  assert.deepEqual(byId.get("1").result.tools.map((item) => item.name), ["deep_web_search", "fetch_content", "get_search_content", "source_check"]);
  assert.equal(byId.get("2").result.isError, true);
  assert.match(byId.get("2").result.content[0].text, /requires query/);
  await writeFile(join(root, "marker"), "ok");
});

test("all provider toggles are enforced before network access", async () => {
  const extensionRoot = await mkdtemp(join(tmpdir(), "ki-deep-web-search-config-"));
  await writeFile(join(extensionRoot, "config.json"), JSON.stringify({ providerToggles: { codex: false, exa: false, tinyfish: false, duckduckgo: false } }));
  const output = run([
    { jsonrpc: "2.0", id: 1, method: "tool.execute", params: { sessionId: "s1", name: "deep_web_search", args: { query: "toggle test" } } },
    { jsonrpc: "2.0", id: 2, method: "shutdown", params: {} },
  ], extensionRoot);
  const byId = new Map(output.map((item) => [String(item.id), item]));
  assert.equal(byId.get("1").result.isError, true);
  assert.match(byId.get("1").result.content[0].text, /disabled|credentials|provider/i);
});
