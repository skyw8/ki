import { invalidateConfig, loadConfig, type ZvecConfig } from "./config.js";
import { COMMANDS, COMMAND_NAMES, invokeCommand } from "./commands.js";
import { EnginePool } from "./engine.js";
import { Host } from "./host.js";
import { IndexJobs, type IndexJobState, notifyText } from "./index-job.js";
import { StdioRpc, safeError } from "./rpc.js";
import { cancelledResult, executeSearch, SEARCH_TOOL_SPEC, type SearchPlan, type ToolResult } from "./tool.js";

const rpc = new StdioRpc();
const pool = new EnginePool();
const sessions = new Map<string, { cwd: string }>();
const hostBySession = new Map<string, Host>();
/** In-flight tool calls keyed by the JSON-RPC id the Host cancels by. */
const activeSearches = new Map<string, { root: string }>();

function hostFor(sessionId: string): Host {
  let host = hostBySession.get(sessionId);
  if (!host) {
    host = new Host(rpc, sessionId);
    hostBySession.set(sessionId, host);
  }
  return host;
}

const jobs = new IndexJobs((sessionId, root, state, config) => {
  void finishIndex(sessionId, root, state, config);
});

async function finishIndex(sessionId: string, root: string, state: IndexJobState, config: ZvecConfig) {
  const host = hostFor(sessionId);
  const finished = state.finished;
  if (!finished) return;
  try {
    await host.appendEntry("zvec-grep-index", {
      root,
      ok: finished.ok,
      files: finished.files ?? null,
      entities: finished.entities ?? null,
      seconds: finished.seconds ?? null,
    });
  } catch (error) {
    process.stderr.write(`zvec-grep appendEntry: ${safeError(error)}\n`);
  }
  if (!finished.ok || !config.notifyOnIndexComplete) return;
  try {
    // The finished index is only useful once the model knows, but a silent extra
    // turn is intrusive, so this message is opt-in through config.
    await host.enqueue(notifyText(state), { when: "settled", idempotencyKey: `zvec-grep-index:${root}` });
  } catch (error) {
    process.stderr.write(`zvec-grep enqueue: ${safeError(error)}\n`);
  }
}

rpc.onRequest(async (method, params, id) => {
  switch (method) {
    case "initialize":
      return initialize();
    case "session.open":
      return sessionOpen(params);
    case "session.close": {
      const sessionId = sessionIdOf(params);
      sessions.delete(sessionId);
      hostBySession.delete(sessionId);
      return {};
    }
    case "tool.execute":
      return toolExecute(params, id);
    case "command.invoke":
      return commandInvoke(params);
    case "cancel":
      return cancelSearch(params);
    case "config.updated":
      return configUpdated();
    case "shutdown":
      await pool.dispose();
      return {};
    default:
      return {};
  }
});

rpc.start();

function initialize() {
  return {
    tools: [SEARCH_TOOL_SPEC],
    commands: COMMANDS.map((command) => ({
      name: command.name,
      description: command.description,
      argumentHint: command.argumentHint,
      completions: COMMAND_NAMES.filter((name) => name !== command.name),
    })),
    subscriptions: [],
  };
}

function sessionOpen(params: unknown) {
  const record = asRecord(params);
  const sessionId = str(record.sessionId);
  const cwd = str(record.cwd) || process.cwd();
  if (sessionId) sessions.set(sessionId, { cwd });
  return { root: cwd };
}

async function toolExecute(params: unknown, id?: string | number) {
  const record = asRecord(params);
  const sessionId = str(record.sessionId);
  if (str(record.name) !== SEARCH_TOOL_SPEC.name) {
    return errorResult(`unknown tool ${str(record.name)}`);
  }
  const cwd = sessions.get(sessionId)?.cwd ?? process.cwd();
  const key = id === undefined || id === null ? "" : String(id);
  const pending = { root: cwd };
  if (key) activeSearches.set(key, pending);
  try {
    return await executeSearch(asRecord(record.args), {
      cwd,
      config: config(),
      pool,
      // Record the resolved root so a cancel answered mid-flight names the
      // workspace it was actually searching.
      onPlan: (plan: SearchPlan) => {
        pending.root = plan.root;
      },
    });
  } finally {
    if (key) activeSearches.delete(key);
  }
}

/**
 * cancelSearch answers a pending tool call immediately. The Host asks for a
 * cancel and then waits a short grace period for a result; replying with an
 * explicit cancellation beats letting the search run into the hard timeout, and
 * StdioRpc suppresses the later reply from the abandoned handler.
 */
function cancelSearch(params: unknown) {
  const id = asRecord(params).id;
  const key = id === undefined || id === null ? "" : String(id);
  const pending = key ? activeSearches.get(key) : undefined;
  if (!pending) return {};
  rpc.replyOnce(key, cancelledResult({ root: pending.root }));
  return {};
}

function configUpdated() {
  invalidateConfig();
  // Embedding/device changes need a new engine; dropping it here keeps the next
  // search honest instead of silently reusing the previous model.
  void pool.dispose();
  return {};
}

async function commandInvoke(params: unknown) {
  const record = asRecord(params);
  const sessionId = str(record.sessionId);
  const name = str(record.name);
  if (!COMMAND_NAMES.includes(name)) return { handled: false };
  return invokeCommand(name, str(record.args), {
    cwd: sessions.get(sessionId)?.cwd ?? process.cwd(),
    host: hostFor(sessionId),
    jobs,
  });
}

function config(): ZvecConfig {
  return loadConfig();
}

function errorResult(message: string): ToolResult {
  return { content: [{ type: "text", text: message }], isError: true };
}

function sessionIdOf(params: unknown): string {
  return str(asRecord(params).sessionId);
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  return {};
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}
