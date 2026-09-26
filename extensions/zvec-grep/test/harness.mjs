// Shared harness: spawns the built sidecar and speaks its NDJSON protocol.
// The runtime tests stub the library and CLI; the live test points at the real
// @zvec/zvec-grep package installed in this extension.
import { spawn } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const extensionPath = dirname(dirname(fileURLToPath(import.meta.url)));
const runtimePath = join(extensionPath, "dist", "main.js");
const fakeLibrary = join(extensionPath, "test", "fixtures", "fake-library.mjs");
const fakeCli = join(extensionPath, "test", "fixtures", "fake-zg.sh");

/**
 * startSidecar launches the built sidecar against the stubbed library and CLI.
 * Host calls (ui.setStatus, session.enqueue, …) are recorded and answered so the
 * sidecar can finish its work.
 */
export function startSidecar(t, options = {}) {
  const root = mkdtempSync(join(tmpdir(), "ki-zvec-grep-"));
  chmodSync(fakeCli, 0o755);
  const workspace = join(root, "workspace");
  mkdirSync(workspace, { recursive: true });
  const log = join(root, "log.ndjson");
  const statePath = join(root, "state.json");
  writeFileSync(log, "");
  writeFileSync(statePath, JSON.stringify(options.state ?? { items: [] }));
  const child = spawn(process.execPath, [runtimePath], {
    cwd: extensionPath,
    env: {
      ...process.env,
      KI_HOME: root,
      // The stub package lives in the temp home; the real one has to resolve
      // from this extension's own node_modules, and its global state stays in
      // the temp home so a live run never touches ~/.zvec-grep.
      KI_EXTENSION_ROOT: options.fake === false ? extensionPath : root,
      ...(options.fake === false ? { ZVEC_GREP_HOME: join(root, "zvec-home") } : {}),
      ...(options.fake === false
        ? {}
        : {
            KI_ZVEC_GREP_LIB: fakeLibrary,
            KI_ZVEC_GREP_CLI: fakeCli,
            KI_ZVEC_GREP_TEST_LOG: log,
            KI_ZVEC_GREP_TEST_STATE: statePath,
            KI_ZVEC_GREP_TEST_HANG: options.hang ? "1" : "",
          }),
      ...(options.env ?? {}),
    },
    stdio: ["pipe", "pipe", "pipe"],
  });
  t.after(() => child.kill("SIGKILL"));

  const inbound = [];
  const messages = [];
  const waiters = new Map();
  const inboundWaiters = [];
  const replies = new Map();
  let buffer = "";
  let stderr = "";

  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    for (;;) {
      const index = buffer.indexOf("\n");
      if (index < 0) break;
      const line = buffer.slice(0, index).trim();
      buffer = buffer.slice(index + 1);
      if (!line) continue;
      let message;
      try {
        message = JSON.parse(line);
      } catch {
        continue;
      }
      messages.push(message);
      if (typeof message.method === "string" && message.id !== undefined) {
        inbound.push({ method: message.method, params: message.params ?? {} });
        const answer = replies.has(message.method) ? replies.get(message.method) : { ok: true };
        child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id: message.id, result: answer })}\n`);
        for (const waiter of [...inboundWaiters]) waiter();
        continue;
      }
      const waiter = waiters.get(String(message.id));
      if (waiter) {
        waiters.delete(String(message.id));
        waiter(message);
      }
    }
  });
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });

  let seq = 1;
  function send(message) {
    child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
  }
  function call(method, params) {
    const id = `t${seq++}`;
    const promise = new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`timeout waiting for ${method} (${stderr})`)), 20_000);
      waiters.set(id, (message) => {
        clearTimeout(timer);
        resolve(message);
      });
    });
    send({ id, method, params });
    return promise;
  }
  async function waitForInbound(predicate, label, timeoutMs = 15_000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const found = inbound.find(predicate);
      if (found) return found;
      if (Date.now() > deadline) throw new Error(`timeout waiting for ${label}; saw ${JSON.stringify(inbound)}\n${stderr}`);
      await new Promise((resolve) => {
        inboundWaiters.push(resolve);
        setTimeout(resolve, 100);
      });
    }
  }
  function entries() {
    return readFileSync(log, "utf8")
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  }
  async function waitForMessages(predicate, label) {
    const deadline = Date.now() + 15_000;
    for (;;) {
      const found = messages.filter(predicate);
      if (found.length) return found;
      if (Date.now() > deadline) throw new Error(`timeout waiting for ${label}\n${stderr}`);
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  async function waitForLog(predicate, label) {
    const deadline = Date.now() + 15_000;
    for (;;) {
      const found = entries().find(predicate);
      if (found) return found;
      if (Date.now() > deadline) throw new Error(`timeout waiting for ${label}; log ${JSON.stringify(entries())}\n${stderr}`);
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  function writeConfig(config) {
    writeFileSync(join(root, "config.json"), JSON.stringify(config));
  }

  return { root, workspace, child, send, call, inbound, messages, waitForInbound, waitForMessages, waitForLog, entries, writeConfig, stderr: () => stderr, replies };
}

