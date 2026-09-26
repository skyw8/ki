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

/**
 * writeBusyError mirrors upstream's DAEMON_LEASE_ACTIVE context verbatim,
 * including the `pid=<n>` line and the CLI-only `client.mode` hint: pid 0 means
 * no daemon lease exists and the guard was merely contended, which is the case
 * the sidecar has to translate.
 */
function writeBusyError(root, pid) {
  const error = new Error("A zvec-grep daemon owns index writes for this root");
  error.name = "EngineError";
  error.code = "ZVEC_GREP.ENGINE.DAEMON_LEASE_ACTIVE";
  error.context = [
    `root=${root}`,
    `pid=${pid}`,
    "hint=Run with --mode auto so a ready daemon handles indexed operations.",
    'config=Edit ~/.zvec-grep/config.json and set client.mode to "auto" to persist this behavior.',
  ].join("\n");
  return error;
}

/**
 * lockBusyError mirrors the library's workspace write-lock refusal, which its
 * read path raises whenever any writer holds that lock.
 */
function lockBusyError(root, ownerOperation) {
  const error = new Error("Index unavailable");
  error.name = "EngineError";
  error.code = "ZVEC_GREP.ENGINE.LOCK.BUSY";
  error.context = [
    `lock=${root}/.zvec-grep/locks/home.write`,
    "operation=context",
    `ownerOperation=${ownerOperation}`,
    "ownerPid=5150",
    "ownerHost=stub",
  ].join("\n");
  return error;
}

/** Emulates the workspace write lock of one in-flight writer, process-wide. */
let writerLockHeld = 0;
let lockBusyCalls = 0;

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

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
      // indexWrite=daemon always refuses (a real daemon owns the root);
      // indexWrite=always refuses with pid 0 (permit held, no daemon lease);
      // indexWrite=contended refuses only while the search refreshes, which is
      // how the library behaves when another writer holds the permit.
      const writeLock = scripted.indexWrite;
      const refreshing = contextOptions.autoUpdate !== false;
      if (writeLock === "daemon" || writeLock === "always" || (writeLock === "contended" && refreshing)) {
        throw writeBusyError(contextOptions.root, writeLock === "daemon" ? 4242 : 0);
      }
      // holdWriterLockMs makes a refreshing call hold the workspace write lock
      // while it runs and makes any overlapping call fail the way the library
      // does, so the sidecar's serialization and retry policy are exercised
      // against a real collision.
      if (scripted.holdWriterLockMs) {
        if (writerLockHeld > 0) throw lockBusyError(contextOptions.root, "context.refresh");
        if (refreshing) {
          writerLockHeld += 1;
          try {
            await sleep(scripted.holdWriterLockMs);
          } finally {
            writerLockHeld -= 1;
          }
        }
      }
      // lockBusy=once fails the first call only; lockBusy=always keeps failing.
      if (scripted.lockBusy && (scripted.lockBusy === "always" || lockBusyCalls++ === 0)) {
        throw lockBusyError(contextOptions.root, scripted.lockOwnerOperation ?? "index");
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
