// Stub of @zvec/zvec-grep used by the sidecar runtime tests. It records every
// createZvecGrep call and answers context()/info() from the scripted state in
// KI_ZVEC_GREP_TEST_STATE, so the sidecar's protocol behavior can be tested
// without the native package or a real index.
import { appendFileSync, readFileSync } from "node:fs";

function state() {
  const path = process.env.KI_ZVEC_GREP_TEST_STATE || "";
  if (!path) return {};
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return {};
  }
}

function log(entry) {
  const path = process.env.KI_ZVEC_GREP_TEST_LOG || "";
  if (!path) return;
  appendFileSync(path, `${JSON.stringify(entry)}\n`);
}

function engineError(message, code) {
  const error = new Error(message);
  error.name = "EngineError";
  error.code = code;
  error.context = "hint=built by the test stub";
  return error;
}

export async function createZvecGrep(options = {}) {
  log({ event: "create", options });
  const scripted = state();
  return {
    root: options.root ?? process.cwd(),
    async info(infoOptions = {}) {
      log({ event: "info", options: infoOptions });
      return {
        root: infoOptions.root ?? options.root ?? process.cwd(),
        indexed: scripted.indexed !== false,
        indexPolicy: scripted.indexed === false ? "undecided" : "enabled",
        home: "/tmp/zvec-home",
        indexPath: "/tmp/zvec-home/index.zvec",
        source: scripted.indexed === false ? "unindexed" : "index",
      };
    },
    async context(contextOptions = {}) {
      log({ event: "context", options: contextOptions });
      if (process.env.KI_ZVEC_GREP_TEST_HANG === "1") {
        await new Promise((resolve) => setTimeout(resolve, 60_000));
      }
      if (scripted.indexed === false) {
        throw engineError("No zvec-grep index found for this workspace", "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_NOT_FOUND");
      }
      if (scripted.disabled === true) {
        throw engineError("The zvec-grep index is disabled for this workspace", "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_DISABLED");
      }
      if (scripted.failure) throw new Error(scripted.failure);
      const items = (scripted.items ?? []).map((item, index) => ({
        kind: "indexed_entity",
        rank: index + 1,
        file: { absolutePath: `${contextOptions.root}/${item.path}`, relativePath: item.path },
        range: { kind: "text", startLine: item.startLine ?? 1, endLine: item.endLine ?? 1 },
        content: item.content ?? "",
        outline: item.outline,
        status: item.status ?? "fresh",
        score: item.score,
        matchedBy: item.matchedBy ?? "fts",
        metadata: item.metadata,
      }));
      return {
        query: (contextOptions.queries ?? ["?"])[0],
        root: contextOptions.root,
        source: "index",
        coverage: "ranked_sample",
        workspaceIndex: { id: "ws-1", name: "stub", path: "/tmp/zvec-home" },
        items,
        diagnostics: {
          index: { hitsReturned: items.length, routes: [{ id: "fts", mode: "fts", query: (contextOptions.queries ?? ["?"])[0] }] },
        },
      };
    },
    async index() {
      return { root: options.root, files: 1, entities: 1, durationMs: 5 };
    },
    async dropIndex() {
      return true;
    },
    async close() {
      log({ event: "close", options });
    },
  };
}
