import { GoalApp } from "./app.js";
import { COMPLETIONS } from "./command.js";
import { Host } from "./host.js";
import { StdioRpc } from "./rpc.js";
import { TOOL_SPECS } from "./tools.js";

const rpc = new StdioRpc();
const apps = new Map<string, GoalApp>();
const home = process.env.KI_HOME || "";

async function prepareApp(sessionId: string): Promise<GoalApp | undefined> {
  if (!sessionId) return undefined;
  const existing = apps.get(sessionId);
  if (existing) return existing;
  const app = new GoalApp(new Host(rpc, sessionId), home, sessionId);
  apps.set(sessionId, app);
  if (await app.restore()) app.scheduleRestoreContinue();
  return app;
}

function sessionIdOf(params: unknown): string {
  return str(asRecord(params).sessionId);
}

rpc.onRequest(async (method, params) => {
  switch (method) {
    case "initialize":
      return initialize(params);
    case "session.open":
      await prepareApp(sessionIdOf(params));
      return {};
    case "session.close": {
      const sessionId = sessionIdOf(params);
      const app = apps.get(sessionId);
      app?.close();
      apps.delete(sessionId);
      return {};
    }
    case "shutdown":
      return {};
    case "command.invoke":
      return invokeCommand(params);
    case "lifecycle.invoke":
      return lifecycleInvoke(params);
    case "lifecycle.event":
      return lifecycleEvent(params);
    case "tool.execute":
      return toolExecute(params);
    case "ui.action":
      return uiAction(params);
    case "ui.submit":
      return uiSubmit(params);
    case "cancel":
      return {};
    default:
      return {};
  }
});

rpc.start();

async function initialize(params: unknown) {
  const p = asRecord(params);
  // Global sidecars receive an empty initialize sessionId. A non-empty value
  // is retained for direct sidecar tests and older embedded launches.
  const sessionId = str(p.sessionId) || process.env.KI_SESSION_ID || "";
  if (sessionId) await prepareApp(sessionId);
  return {
    tools: TOOL_SPECS,
    commands: [
      {
        name: "goal",
        description: "Run a goal to completion",
        argumentHint: "<objective>",
        completions: COMPLETIONS,
      },
    ],
    subscriptions: [
      { event: "before_agent_start", mode: "sync" },
      { event: "agent_settled", mode: "async" },
    ],
  };
}

function invokeCommand(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (str(p.name) !== "goal" || !app) return { handled: false };
  return app.invokeCommand(str(p.args));
}

function lifecycleInvoke(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (str(p.event) !== "before_agent_start" || !app) return {};
  return app.onBeforeAgentStart(asRecord(p.payload));
}

function lifecycleEvent(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (str(p.event) === "agent_settled" && app) void app.onAgentSettled();
  return {};
}

function toolExecute(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (!app) return { content: [{ type: "text", text: "goal sidecar not ready" }], isError: true };
  return app.executeTool(str(p.name), asRecord(p.args));
}

function uiAction(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (app) void app.onAction(str(p.id));
  return {};
}

function uiSubmit(params: unknown) {
  const p = asRecord(params);
  const app = apps.get(str(p.sessionId));
  if (app) void app.onSubmit(asRecord(p.fields));
  return {};
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  return {};
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}
