import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { startSidecar } from "./harness.mjs";

function searchArgs(overrides = {}) {
  return { query: "where is the theme persisted", ...overrides };
}

test("initialize publishes the search tool, commands, and no credential fields", async (t) => {
  const sidecar = startSidecar(t);
  const response = await sidecar.call("initialize", {});
  const tools = response.result.tools;
  assert.deepEqual(tools.map((tool) => tool.name), ["zvec_grep_search"]);
  assert.equal(tools[0].parameters.additionalProperties, false);
  assert.equal(tools[0].parameters.properties.apiKey, undefined);
  assert.equal(tools[0].parameters.properties.device, undefined);
  assert.equal(tools[0].timeoutMs, 120_000);
  assert.deepEqual(response.result.commands.map((command) => command.name), ["zg-index", "zg-status", "zg-remove"]);
});

test("search resolves the session cwd and renders ranked file:line anchors", async (t) => {
  const sidecar = startSidecar(t, {
    state: {
      items: [
        { path: "src/theme.ts", startLine: 1, endLine: 3, content: "function useTheme() {\n  const [theme] = useState('light');\n}", matchedBy: "fts+vector", score: 0.5, metadata: { symbolType: "function" } },
      ],
    },
  });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.notEqual(result.isError, true);
  assert.match(result.content[0].text, /freshness: fresh/);
  assert.match(result.content[0].text, new RegExp(`root: ${sidecar.workspace.replace(/[/\\]/g, "\\$&")}`));
  assert.match(result.content[0].text, /#1 rank=1 matchedBy=fts\+vector score=0\.5000 src\/theme\.ts:1-3/);
  assert.match(result.content[0].text, /1\tfunction useTheme\(\) \{/);
  assert.equal(result.details.items, 1);

  const contextCall = sidecar.entries().find((entry) => entry.event === "context");
  assert.equal(contextCall.options.root, sidecar.workspace);
  assert.deepEqual(contextCall.options.queries, ["where is the theme persisted"]);
  assert.equal(contextCall.options.autoUpdate, true);
  assert.equal(contextCall.options.limit, 5);
});

test("unindexed workspaces return an actionable error instead of building an index", async (t) => {
  const sidecar = startSidecar(t, { state: { indexed: false } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  assert.equal(response.result.isError, true);
  assert.match(response.result.content[0].text, /No zvec-grep index/);
  assert.match(response.result.content[0].text, /\/zg-index/);
  assert.equal(response.result.details.code, "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_NOT_FOUND");
  assert.equal(sidecar.entries().some((entry) => entry.event === "cli" && /index /.test(entry.args)), false);
});

test("preview short truncates content while full keeps the outline", async (t) => {
  const lines = Array.from({ length: 20 }, (_, index) => `line ${index + 1}`).join("\n");
  const sidecar = startSidecar(t, {
    state: { items: [{ path: "docs/design.md", startLine: 1, endLine: 20, content: lines, outline: "# Design\n## Risks", metadata: { heading: "Design", level: 1 } }] },
  });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const short = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs({ preview: "short" }) });
  assert.match(short.result.content[0].text, /more lines/);
  assert.doesNotMatch(short.result.content[0].text, /outline:/);
  assert.match(short.result.content[0].text, /heading: Design \(level 1\)/);
  const full = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs({ preview: "full" }) });
  assert.match(full.result.content[0].text, /outline:/);
  assert.match(full.result.content[0].text, /20\tline 20/);
});

test("configured ignoredGlobs are always applied", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ ignoredGlobs: ["**/dist/**"] });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("tool.execute", {
    sessionId: "s1",
    name: "zvec_grep_search",
    args: searchArgs({ fts: ["theme"], vector: ["theme storage"], fuse: true, limit: 3, globs: ["src/**"], freshness: "eventual" }),
  });
  const contextCall = sidecar.entries().find((entry) => entry.event === "context");
  assert.deepEqual(contextCall.options.excludePaths, ["**/dist/**"]);
  assert.deepEqual(contextCall.options.globs, ["src/**"]);
  assert.deepEqual(contextCall.options.routes, [
    { mode: "fts", query: "theme" },
    { mode: "vector", query: "theme storage" },
  ]);
  assert.equal(contextCall.options.limit, 3);
  assert.equal(contextCall.options.autoUpdate, false);
});

test("config.updated rebuilds the engine with the new embedding model", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ embedding: "local/first" });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });

  sidecar.writeConfig({ embedding: "local/second" });
  await sidecar.call("config.updated", { config: { embedding: "local/second" } });
  await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });

  const creates = sidecar.entries().filter((entry) => entry.event === "create");
  assert.deepEqual(creates.map((entry) => entry.options.embedding), ["local/first", "local/second"]);
  assert.equal(sidecar.entries().some((entry) => entry.event === "close"), true);
});

test("cancel answers the pending call once and suppresses the late result", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] }, hang: true });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  sidecar.send({ id: 4242, method: "tool.execute", params: { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() } });
  // The stub logs the context call before hanging, so this waits for the search
  // to be genuinely in flight.
  await sidecar.waitForLog((entry) => entry.event === "context", "the search to start");
  sidecar.send({ method: "cancel", params: { id: 4242, sessionId: "s1" } });

  const replies = await sidecar.waitForMessages((message) => String(message.id) === "4242", "the cancelled reply");
  // The abandoned handler is still sleeping in the library; give it room to
  // (wrongly) answer a second time before asserting the reply count.
  await new Promise((resolve) => setTimeout(resolve, 300));
  assert.equal(replies.length, 1, `expected exactly one reply, got ${JSON.stringify(replies)}`);
  assert.equal(replies[0].result.isError, true);
  assert.match(replies[0].result.content[0].text, /was cancelled/);
  assert.equal(replies[0].result.details.root, sidecar.workspace);
});

test("/zg-status reports the CLI status and the running job", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-status", args: "" });
  assert.equal(response.result.handled, true);
  assert.match(response.result.notice, /Workspace index is ready/);
  assert.equal(sidecar.entries().some((entry) => entry.event === "cli" && entry.args === `status ${sidecar.workspace} --mode direct`), true);
});

test("/zg-index runs in the background, reports progress, and notifies when asked", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ notifyOnIndexComplete: true });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.equal(response.result.handled, true);
  assert.match(response.result.notice, /Started/);

  await sidecar.waitForInbound((entry) => entry.method === "session.enqueue", "the completion notification");
  const statuses = sidecar.inbound.filter((entry) => entry.method === "ui.setStatus").map((entry) => entry.params.text);
  assert.equal(statuses.some((text) => text && text.key === "index.scanning"), true);
  assert.equal(statuses.some((text) => text && text.key === "index.progress" && text.params.total === 2), true);
  assert.equal(statuses.some((text) => text && text.key === "index.done"), true);
  const completion = sidecar.inbound.find((entry) => entry.method === "session.enqueue");
  assert.match(completion.params.content[0].text, /is ready/);
  assert.equal(completion.params.kind, "custom");
  const appended = sidecar.inbound.find((entry) => entry.method === "session.appendEntry");
  assert.equal(appended.params.customType, "zvec-grep-index");
  assert.equal(appended.params.data.files, 2);
  assert.equal(
    sidecar.entries().some((entry) => entry.event === "cli" &&
      entry.args === `index ${sidecar.workspace} --embedding local/potion-code-16m-v2 --device auto --mode direct`),
    true,
  );
});

test("/zg-index forwards the configured embedding model and device", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ embedding: "local/jina-embeddings-v2-base-code", device: "cpu" });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.match(response.result.notice, /--embedding local\/jina-embeddings-v2-base-code/);
  const invocation = await sidecar.waitForLog(
    (entry) => entry.event === "cli" && /^index /.test(entry.args),
    "the zg index invocation",
  );
  assert.equal(
    invocation.args,
    `index ${sidecar.workspace} --embedding local/jina-embeddings-v2-base-code --device cpu --mode direct`,
  );
});

test("/zg-index lets its own flags win over the settings", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ embedding: "local/jina-embeddings-v2-base-code", device: "cpu" });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "--embedding local/other --device=metal" });
  const invocation = await sidecar.waitForLog(
    (entry) => entry.event === "cli" && /^index /.test(entry.args),
    "the zg index invocation",
  );
  assert.equal(invocation.args, `index ${sidecar.workspace} --embedding local/other --device=metal --mode direct`);
});

test("a remote embedding model is forwarded without a device", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  // zg rejects a device for a remote provider, so neither path may send one.
  sidecar.writeConfig({ embedding: "qwen/text-embedding-v4", device: "cpu" });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });

  const create = await sidecar.waitForLog((entry) => entry.event === "create", "the engine creation");
  assert.equal(create.options.embedding, "qwen/text-embedding-v4");
  assert.equal("device" in create.options, false);
  const invocation = await sidecar.waitForLog(
    (entry) => entry.event === "cli" && /^index /.test(entry.args),
    "the zg index invocation",
  );
  assert.equal(invocation.args, `index ${sidecar.workspace} --embedding qwen/text-embedding-v4 --mode direct`);
});

test("/zg-index failure names the slash command that rebuilds the index", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] }, env: { KI_ZVEC_GREP_FAKE_INDEX_FAIL: "1" } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  const failed = await sidecar.waitForInbound(
    (entry) => entry.method === "ui.setStatus" && entry.params.text?.key === "index.failed",
    "the failure status",
  );
  assert.match(failed.params.text.fallback, /\/zg-index --rebuild/);
});

test("/zg-index refuses a second job and does not accept --drop", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const first = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.match(first.result.notice, /Started/);
  const second = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.match(second.result.notice, /already running/);
  const dropped = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "--drop" });
  assert.match(dropped.result.notice, /use \/zg-remove/);
});

test("a contended index write lock retries, then searches without refreshing", async (t) => {
  const sidecar = startSidecar(t, {
    state: {
      indexWrite: "contended",
      items: [{ path: "src/theme.ts", startLine: 1, endLine: 1, content: "const theme = 'light'", matchedBy: "fts" }],
    },
  });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.notEqual(result.isError, true);
  assert.match(result.content[0].text, /^note: refresh skipped — another writer holds the index write lock/);
  assert.match(result.content[0].text, /freshness: fresh/);
  assert.match(result.content[0].text, /src\/theme\.ts:1-1/);
  assert.equal(result.details.refreshSkipped, "write_busy");
  assert.equal(result.details.attempts, 3);
  const contexts = sidecar.entries().filter((entry) => entry.event === "context");
  assert.deepEqual(contexts.map((entry) => entry.options.autoUpdate), [true, true, false]);
});

test("an eventual search never contends with the index write lock", async (t) => {
  const sidecar = startSidecar(t, { state: { indexWrite: "contended", items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", {
    sessionId: "s1",
    name: "zvec_grep_search",
    args: searchArgs({ freshness: "eventual" }),
  });
  assert.notEqual(response.result.isError, true);
  assert.doesNotMatch(response.result.content[0].text, /refresh skipped/);
  assert.equal(response.result.details.attempts, 1);
});

test("a daemon-owned root is named instead of the library's CLI hint", async (t) => {
  const sidecar = startSidecar(t, { state: { indexWrite: "daemon", items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /a zvec-grep daemon owns the index writes/);
  assert.match(result.content[0].text, /pid 4242/);
  assert.match(result.content[0].text, /zg server off/);
  // The library's own hint is about the CLI's transport mode, which an
  // in-process search cannot set; relaying it would misdirect the model.
  assert.doesNotMatch(result.content[0].text, /client\.mode/);
  assert.equal(result.details.holder, "daemon");
  assert.equal(result.details.holderPid, 4242);
});

test("a write lock held without a daemon lease is reported as contention", async (t) => {
  const sidecar = startSidecar(t, { state: { indexWrite: "always", items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.equal(result.isError, true);
  // pid=0 means no daemon lease exists: only the guard was contended.
  assert.match(result.content[0].text, /another writer holds the zvec-grep index write lock/);
  assert.match(result.content[0].text, /freshness=eventual/);
  assert.doesNotMatch(result.content[0].text, /daemon owns/);
  assert.doesNotMatch(result.content[0].text, /config\.json/);
  assert.equal(result.details.holder, "unknown");
  assert.equal(result.details.attempts, 3);
});

test("a running index job makes searches skip the refresh", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] }, env: { KI_ZVEC_GREP_FAKE_INDEX_SLEEP: "2" } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const started = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.match(started.result.notice, /Started/);

  const refreshed = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  assert.notEqual(refreshed.result.isError, true);
  assert.match(refreshed.result.content[0].text, /^note: refresh skipped — an index job is running for this root/);
  assert.equal(refreshed.result.details.refreshSkipped, "index_job");
  assert.equal(sidecar.entries().find((entry) => entry.event === "context").options.autoUpdate, false);
});

test("concurrent searches of one sidecar do not collide on the write lock", async (t) => {
  // The stub makes the first refreshing search hold the workspace write lock for
  // 1.5s and fails any overlapping call, which is what the real library does; a
  // serialized second search therefore has to wait, not retry its way through.
  const sidecar = startSidecar(t, {
    state: { holdWriterLockMs: 1500, items: [{ path: "src/theme.ts", startLine: 1, endLine: 1, content: "theme", matchedBy: "fts" }] },
  });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const startedAt = Date.now();
  const [first, second] = await Promise.all([
    sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() }),
    sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs({ query: "another question" }) }),
  ]);
  for (const response of [first, second]) {
    assert.notEqual(response.result.isError, true, JSON.stringify(response.result));
    assert.equal(response.result.details.attempts, 1);
  }
  assert.equal(second.result.details.refreshSkipped, null);
  assert.ok(Date.now() - startedAt >= 1500, "the second search must wait for the first");
  const contexts = sidecar.entries().filter((entry) => entry.event === "context");
  assert.equal(contexts.length, 2);
  assert.equal(contexts.some((entry) => entry.options.autoUpdate === false), false);
});

test("a workspace write lock held elsewhere is retried, then named", async (t) => {
  const sidecar = startSidecar(t, { state: { lockBusy: "once", items: [] } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const recovered = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  assert.notEqual(recovered.result.isError, true);
  assert.equal(recovered.result.details.attempts, 2);

  const sidecar2 = startSidecar(t, { state: { lockBusy: "always", items: [] } });
  await sidecar2.call("session.open", { sessionId: "s1", cwd: sidecar2.workspace });
  const response = await sidecar2.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /another writer holds the zvec-grep workspace write lock/);
  assert.match(result.content[0].text, /by `index` \(pid 5150\)/);
  assert.match(result.content[0].text, /locks\/home\.write/);
  assert.equal(result.details.holder, "index");
  assert.equal(result.details.holderPid, 5150);
  assert.equal(result.details.retryable, true);
  assert.equal(result.details.attempts, 3);
});

test("a search that loses to the sidecar's own index job says so", async (t) => {
  const sidecar = startSidecar(t, {
    state: { lockBusy: "always", items: [] },
    env: { KI_ZVEC_GREP_FAKE_INDEX_SLEEP: "2" },
  });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  const result = response.result;
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /while an index job is building it/);
  assert.match(result.content[0].text, /use Grep for an exact anchor/);
  assert.equal(result.details.reason, "index_job");
  // Waiting for a build would burn the search budget: one attempt, then report.
  assert.equal(result.details.attempts, 1);
});

test("/zg-remove asks for confirmation and forwards the drop", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.replies.set("ui.confirm", { ok: false });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const declined = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-remove", args: "" });
  assert.match(declined.result.notice, /Cancelled/);
  assert.equal(sidecar.entries().some((entry) => entry.event === "cli" && /--drop/.test(entry.args)), false);

  sidecar.replies.set("ui.confirm", { ok: true });
  const confirmed = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-remove", args: "" });
  assert.equal(confirmed.result.handled, true);
  assert.equal(
    sidecar.entries().some((entry) => entry.event === "cli" && entry.args === `index ${sidecar.workspace} --drop --yes --mode direct`),
    true,
  );
});
