import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

type Msg = {
  jsonrpc?: string;
  id?: number | string;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { message?: string };
};

function startSidecar(home: string, sessionId: string) {
  const child = spawn("node", ["--import", "tsx", "src/index.ts"], {
    cwd: root,
    env: { ...process.env, KI_HOME: home, KI_SESSION_ID: sessionId, KI_EXTENSION: "goal" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const buf = { text: "" };
  const waiters: Array<(msg: Msg) => void> = [];
  child.stdout?.setEncoding("utf8");
  child.stdout?.on("data", (chunk: string) => {
    buf.text += chunk;
    for (;;) {
      const i = buf.text.indexOf("\n");
      if (i < 0) break;
      const line = buf.text.slice(0, i).trim();
      buf.text = buf.text.slice(i + 1);
      if (!line) continue;
      const msg = JSON.parse(line) as Msg;
      if (msg.method && msg.id !== undefined) {
        // inbound Host call: reply ok so initialize/command can finish
        child.stdin?.write(`${JSON.stringify({ jsonrpc: "2.0", id: msg.id, result: { ok: true, idle: true } })}\n`);
        continue;
      }
      const w = waiters.shift();
      if (w) w(msg);
    }
  });
  function send(obj: unknown) {
    child.stdin?.write(`${JSON.stringify(obj)}\n`);
  }
  function recv(): Promise<Msg> {
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("timeout")), 3000);
      waiters.push((msg) => {
        clearTimeout(t);
        resolve(msg);
      });
    });
  }
  return { child, send, recv };
}

test("initialize declares command, tools, subscriptions", async () => {
  const home = await mkdtemp(join(tmpdir(), "ki-goal-"));
  const s = startSidecar(home, "sess-1");
  try {
    s.send({
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: { sessionId: "sess-1", home, cwd: home, extensionRoot: root, capabilities: ["command", "tool", "lifecycle"] },
    });
    const res = await s.recv();
    assert.equal(res.id, 1);
    const result = res.result as {
      commands: Array<{ name: string; argumentHint?: string; completions?: string[] }>;
      tools: Array<{ name: string }>;
      subscriptions: Array<{ event: string; mode: string }>;
    };
    assert.equal(result.commands[0]?.name, "goal");
    assert.equal(result.commands[0]?.argumentHint, "<objective>");
    assert.deepEqual(result.commands[0]?.completions, ["pause", "resume", "clear", "edit", "status"]);
    assert.deepEqual(result.tools.map((t) => t.name).sort(), [
      "goal_blocked",
      "goal_complete",
      "goal_wait",
    ]);
    assert.deepEqual(result.subscriptions, [
      { event: "before_agent_start", mode: "sync" },
      { event: "agent_settled", mode: "async" },
    ]);
  } finally {
    s.child.kill("SIGTERM");
    await rm(home, { recursive: true, force: true });
  }
});

test("start returns prompt; before_agent_start appends system", async () => {
  const home = await mkdtemp(join(tmpdir(), "ki-goal-"));
  const s = startSidecar(home, "sess-2");
  try {
    s.send({ jsonrpc: "2.0", id: 1, method: "initialize", params: { sessionId: "sess-2", home, capabilities: [] } });
    await s.recv();
    s.send({ jsonrpc: "2.0", id: 2, method: "command.invoke", params: { name: "goal", args: "fix the tests" } });
    const cmd = await s.recv();
    const out = cmd.result as { handled: boolean; prompt?: string };
    assert.equal(out.handled, false);
    assert.match(out.prompt ?? "", /fix the tests/);
    s.send({
      jsonrpc: "2.0",
      id: 3,
      method: "lifecycle.invoke",
      params: { event: "before_agent_start", payload: { system: "BASE SYSTEM" } },
    });
    const life = await s.recv();
    const sys = (life.result as { system?: string }).system ?? "";
    assert.match(sys, /^BASE SYSTEM\n\nActive \/goal:/);
    assert.match(sys, /fix the tests/);
    assert.match(sys, /goal_complete tool stale-turn guard/);
    assert.match(sys, /goal_wait/);
  } finally {
    s.child.kill("SIGTERM");
    await rm(home, { recursive: true, force: true });
  }
});
