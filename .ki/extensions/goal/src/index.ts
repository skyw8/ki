import { GoalApp } from "./app.js";
import { COMPLETIONS } from "./command.js";
import { Host } from "./host.js";
import { StdioRpc } from "./rpc.js";
import { TOOL_SPECS } from "./tools.js";

const rpc = new StdioRpc();
const host = new Host(rpc);

let app: GoalApp | undefined;

rpc.onRequest(async (method, params) => {
  switch (method) {
    case "initialize":
      return initialize(params);
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
  const sessionId = str(p.sessionId) || process.env.KI_SESSION_ID || "";
  const home = str(p.home) || process.env.KI_HOME || "";
  app = new GoalApp(host, home, sessionId);
  if (await app.restore()) app.scheduleRestoreContinue();
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
  if (str(p.name) !== "goal" || !app) return { handled: false };
  return app.invokeCommand(str(p.args));
}

function lifecycleInvoke(params: unknown) {
  const p = asRecord(params);
  if (str(p.event) !== "before_agent_start" || !app) return {};
  return app.onBeforeAgentStart(asRecord(p.payload));
}

function lifecycleEvent(params: unknown) {
  const p = asRecord(params);
  if (str(p.event) === "agent_settled" && app) void app.onAgentSettled();
  return {};
}

function toolExecute(params: unknown) {
  const p = asRecord(params);
  if (!app) return { content: [{ type: "text", text: "goal sidecar not ready" }], isError: true };
  return app.executeTool(str(p.name), asRecord(p.args));
}

function uiAction(params: unknown) {
  const p = asRecord(params);
  if (app) void app.onAction(str(p.id));
  return {};
}

function uiSubmit(params: unknown) {
  const p = asRecord(params);
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
