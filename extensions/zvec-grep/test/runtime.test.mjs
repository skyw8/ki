import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { availableParallelism, tmpdir } from "node:os";
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

test("settings and search limits retain JavaScript coercion and whitespace semantics", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] } });
  sidecar.writeConfig({ version: 1, embedding: "\uFEFFlocal/custom\u3000", device: ["cpu"], mode: ["in-process"], maxResults: "", searchTimeoutMs: ["0x15f90"], ignoredGlobs: ["\uFEFF**/dist/**\u2003", "**/dist/**", "\u0085"] });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  for (const args of [searchArgs({ query: "\uFEFFintent\u3000" }), searchArgs({ limit: ["0x10"] }), searchArgs({ limit: "0b11" }), searchArgs({ limit: [1, 2] })]) {
    const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args });
    assert.notEqual(response.result.isError, true);
  }
  const entries = sidecar.entries();
  const created = entries.find((entry) => entry.event === "create");
  assert.equal(created.options.embedding, "local/custom");
  assert.equal(created.options.device, "cpu");
  const contexts = entries.filter((entry) => entry.event === "context");
  assert.deepEqual(contexts.map((entry) => entry.options.limit), [1, 16, 3, 1]);
  assert.deepEqual(contexts[0].options.queries, ["intent"]);
  assert.deepEqual(contexts[0].options.excludePaths, ["**/dist/**", "\u0085"]);
});

test("invalid or newer config version headers are rejected without rewriting them", async (t) => {
  const sidecar = startSidecar(t);
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  for (const version of ["1", 1.5, true, [], {}, -1, 2]) {
    sidecar.writeConfig({ version });
    await sidecar.call("config.updated", {});
    const response = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
    assert.match(response.error.message, /invalid|newer state version/);
    assert.deepEqual(JSON.parse(readFileSync(join(sidecar.root, "config.json"), "utf8")), { version });
  }
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

test("synchronously blocked native searches leave a control worker responsive", async (t) => {
  // Hosts with five CPUs exercise all four slots; smaller runtimes only have
  // enough workers to block CPU-count minus one searches and retain control.
  const blocked = Math.min(4, availableParallelism() - 1);
  if (blocked === 0) return t.skip("a single-CPU runtime has no spare async worker");
  const blockRoot = mkdtempSync(join(tmpdir(), "ki-zvec-block-"));
  const release = join(blockRoot, "release");
  let sidecar;
  t.after(async () => {
    // Release before terminating the sidecar; only remove the marker directory
    // after exit so a still-running native poll cannot miss the release.
    writeFileSync(release, "");
    if (sidecar && sidecar.child.exitCode === null && sidecar.child.signalCode === null) {
      await new Promise((resolve) => {
        sidecar.child.once("exit", resolve);
        sidecar.child.kill("SIGKILL");
      });
    }
    rmSync(blockRoot, { recursive: true, force: true });
  });
  sidecar = startSidecar(t, { state: { items: [], blockingRelease: release } });
  const roots = Array.from({ length: blocked }, (_, index) => join(sidecar.workspace, `root-${index}`));
  for (const root of roots) mkdirSync(root);
  // Different roots bypass the per-root gate. Each context is observed before
  // cancelling any, so this cannot pass by accidentally serializing all searches.
  for (const [index, root] of roots.entries()) {
    sidecar.send({ id: 5000 + index, method: "tool.execute", params: { name: "zvec_grep_search", args: searchArgs({ root }) } });
  }
  for (const root of roots) {
    await sidecar.waitForLog((entry) => entry.event === "context" && entry.options.root === root, "all blocked searches to start");
  }
  const initialized = await sidecar.call("initialize", {});
  assert.equal(initialized.result.tools[0].name, "zvec_grep_search");
  assert.equal(sidecar.entries().some((entry) => entry.event === "context_unblocked"), false);
  for (const index of roots.keys()) sidecar.send({ method: "cancel", params: { id: 5000 + index } });
  for (const index of roots.keys()) {
    const replies = await sidecar.waitForMessages((message) => message.id === 5000 + index, "the cancelled search");
    assert.equal(replies.length, 1);
    assert.equal(replies[0].result.details.cancelled, true);
  }
  writeFileSync(release, "");
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

test("old index versions name the Ki rebuild command in searches and index failures", async (t) => {
  const error = "unsupported index version 1; expected 2; rebuild the index with `zg --index --rebuild`";
  const sidecar = startSidecar(t, { state: { failure: error, indexFailure: error } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const searched = await sidecar.call("tool.execute", { sessionId: "s1", name: "zvec_grep_search", args: searchArgs() });
  assert.equal(searched.result.isError, true);
  assert.match(searched.result.content[0].text, /\/zg-index --rebuild/);
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  const failed = await sidecar.waitForInbound(
    (entry) => entry.method === "ui.setStatus" && entry.params.text?.key === "index.failed",
    "the version failure status",
  );
  assert.match(failed.params.text.fallback, /\/zg-index --rebuild/);
});

test("a successful job's expiry cannot clear a later failure, which remains until a retry", async (t) => {
  const sidecar = startSidecar(t, { state: { items: [] }, env: {
    KI_ZVEC_GREP_FAKE_INDEX_SLEEP: "0.01", KI_ZVEC_GREP_TEST_STATUS_CLEAR_MS: "300",
  } });
  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  await sidecar.waitForInbound((entry) => entry.method === "session.appendEntry", "first completion");
  writeFileSync(join(sidecar.root, "state.json"), JSON.stringify({ indexFailure: "unsupported index version 1; expected 2; rebuild the index" }));
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  await sidecar.waitForInbound((entry) => entry.method === "ui.setStatus" && entry.params.tone === "error", "second job failure");
  await new Promise((resolve) => setTimeout(resolve, 450));
  const statuses = () => sidecar.inbound.filter((entry) => entry.method === "ui.setStatus");
  assert.equal(statuses().at(-1).params.tone, "error");
  assert.equal(statuses().some((entry) => entry.params.text === ""), false);

  writeFileSync(join(sidecar.root, "state.json"), JSON.stringify({ items: [] }));
  const previous = sidecar.inbound.length;
  await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "--rebuild" });
  await sidecar.waitForInbound((entry) => sidecar.inbound.indexOf(entry) >= previous && entry.method === "ui.setStatus" && entry.params.tone === "success", "successful retry");
  await sidecar.waitForInbound((entry) => entry.method === "ui.setStatus" && entry.params.text === "", "successful retry expiry");
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

test("packaged executable starts without source files or language runtimes", async (t) => {
  const extensionPath = dirname(dirname(fileURLToPath(import.meta.url)));
  const binary = process.env.KI_ZVEC_GREP_RUNTIME || join(extensionPath, "target", "release", process.platform === "win32" ? "zvec-grep.exe" : "zvec-grep");
  const root = mkdtempSync(join(tmpdir(), "ki-zvec-standalone-"));
  const standalone = join(root, process.platform === "win32" ? "zvec-grep.exe" : "zvec-grep");
  const { copyFileSync } = await import("node:fs");
  copyFileSync(binary, standalone);
  chmodSync(standalone, 0o755);
  const sidecar = startSidecar(t, { fake: false, runtimePath: standalone, cwd: root, extensionRoot: root, env: { PATH: "" } });
  const initialized = await sidecar.call("initialize", {});
  assert.equal(initialized.result.tools[0].name, "zvec_grep_search");
  await sidecar.call("session.open", { sessionId: "standalone", cwd: sidecar.workspace });
  const searched = await sidecar.call("tool.execute", { sessionId: "standalone", name: "zvec_grep_search", args: searchArgs() });
  assert.equal(searched.result.details.code, "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_NOT_FOUND");
  const status = await sidecar.call("command.invoke", { sessionId: "standalone", name: "zg-status", args: "" });
  assert.equal(status.result.handled, true);
  assert.doesNotMatch(status.result.notice, /not found|setup|source|bun|node/i);
  sidecar.send({ id: "final-initialize", method: "initialize", params: {} });
  sidecar.child.stdin.end();
  const finalReplies = await sidecar.waitForMessages((message) => message.id === "final-initialize", "the reply after stdin closes");
  assert.equal(finalReplies[0].result.tools[0].name, "zvec_grep_search");
});

test("stdin EOF exits within its deadline when the host stops reading stdout", { timeout: 15_000 }, async (t) => {
  const extensionPath = dirname(dirname(fileURLToPath(import.meta.url)));
  const binary = process.env.KI_ZVEC_GREP_RUNTIME || join(extensionPath, "target", "release", process.platform === "win32" ? "zvec-grep.exe" : "zvec-grep");
  const root = mkdtempSync(join(tmpdir(), "ki-zvec-blocked-output-"));
  const child = spawn(binary, [], { cwd: root, env: { ...process.env, KI_HOME: root, KI_EXTENSION_ROOT: root, PATH: "" }, stdio: ["pipe", "pipe", "pipe"] });
  t.after(() => { child.kill("SIGKILL"); child.stdout.destroy(); child.stderr.destroy(); });
  child.stdin.on("error", (error) => { if (error.code !== "EPIPE") throw error; });
  child.stderr.resume();
  const exited = new Promise((resolve, reject) => { child.once("exit", (code) => resolve(code)); child.once("error", reject); });
  // Each tool schema reply is several KB, so paused stdout fills its OS pipe.
  child.stdin.end(Array.from({ length: 2000 }, (_, id) => JSON.stringify({ jsonrpc: "2.0", id, method: "initialize", params: {} }) + "\n").join(""));
  const deadline = new Promise((_, reject) => { const timer = setTimeout(() => reject(new Error("EOF shutdown was blocked by unread stdout")), 8000); timer.unref(); exited.finally(() => clearTimeout(timer)); });
  assert.equal(await Promise.race([exited, deadline]), 0);
});
