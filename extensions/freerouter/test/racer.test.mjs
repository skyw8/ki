import { test } from "node:test";
import assert from "node:assert/strict";
import { DEFAULTS } from "../dist/config.js";
import { FreeRouter } from "../dist/router.js";
import { runFreeRouterStream } from "../dist/racer.js";
import { fetchByModel, sseResponse, chatChunk } from "./helpers.mjs";

const MODELS = [
  { id: "groq/a:free", name: "A" },
  { id: "cerebras/b:free", name: "B" },
  { id: "other/c:free", name: "C" },
  { id: "other/d:free", name: "D" },
];
const IDS = MODELS.map((m) => m.id);

const BASE_REQUEST = { system: "s", messages: [], tools: [], maxTokens: 100 };

function textStream(text, { delayMs = 0 } = {}) {
  return () =>
    sseResponse(
      [chatChunk({ content: text }), chatChunk({ content: "", finishReason: "stop" }), "[DONE]"],
      { delayMs },
    );
}

/** A response that never produces data (simulates a hung provider). */
const hangForever = () => new Response(new ReadableStream({ start() {} }), { status: 200 });

function setup(handlers, { config = {}, signal } = {}) {
  const router = new FreeRouter(IDS, { exhaustedTtlMs: 10_000, slowTtlMs: 10_000 });
  const pool = { ensure: async () => ({ models: MODELS, router }) };
  const events = [];
  const finished = runFreeRouterStream({
    request: BASE_REQUEST,
    config: {
      ...DEFAULTS,
      firstTokenTimeoutMs: 150,
      idleTimeoutMs: 120,
      ...config,
    },
    pool,
    apiKey: "k",
    emit: (event) => events.push(event),
    signal,
    fetchImpl: fetchByModel(handlers),
  });
  return { events, finished, router };
}

test("races candidates and forwards the first responder with +1 content offset", async () => {
  const { events, finished } = setup({
    "groq/a:free": textStream("fast"),
    "cerebras/b:free": () => hangForever(),
  });
  await finished;

  assert.equal(events[0].type, "thinking_start");
  assert.equal(events[0].contentIndex, 0);
  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Round 1: groq/a:free, cerebras/b:free")));
  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Using groq/a:free")));
  const textDelta = events.find((e) => e.type === "text_delta");
  assert.equal(textDelta.contentIndex, 1, "winner content is remapped after the thinking block");
  const done = events.find((e) => e.type === "done");
  assert.equal(done.message.content[0].type, "thinking");
  assert.ok(done.message.content[0].thinking.includes("Using groq/a:free"));
  assert.equal(done.message.content[1].text, "fast");
  assert.equal(done.message.model, "groq/a:free");
});

test("loser failures go into cooldown and the race continues with other models", async () => {
  const { events, finished, router } = setup({
    "groq/a:free": () => new Response("rate limited", { status: 429 }),
    "cerebras/b:free": textStream("ok"),
  });
  await finished;

  assert.ok(events.some((e) => e.type === "done" && e.message.content[1].text === "ok"));
  assert.equal(events.filter((e) => e.type === "done").length, 1);
  assert.deepEqual(router.nextModels(10).filter((id) => id === "groq/a:free"), [], "429 loser is cooling down");
});

test("first-token timeout marks candidates slow and proceeds through batches", async () => {
  const { events, finished } = setup(
    {
      "groq/a:free": () => hangForever(),
      "cerebras/b:free": () => hangForever(),
      "other/c:free": textStream("late"),
      "other/d:free": () => hangForever(),
    },
    { config: { maxBatches: 2 } },
  );
  await finished;

  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Round 1: groq/a:free, cerebras/b:free")));
  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Round 2: other/c:free, other/d:free")));
  assert.ok(events.some((e) => e.type === "done"));
});

test("pool exhaustion ends with a retryable error after maxBatches", async () => {
  const { events, finished } = setup(
    {
      "groq/a:free": () => hangForever(),
      "cerebras/b:free": () => hangForever(),
      "other/c:free": () => hangForever(),
      "other/d:free": () => hangForever(),
    },
    { config: { maxBatches: 2 } },
  );
  await finished;

  const error = events.find((e) => e.type === "error");
  assert.ok(error.error.includes("All free models exhausted"));
  assert.equal(error.reason, "error");
});

test("402 fatal error surfaces immediately without further batches", async () => {
  const { events, finished } = setup({
    "groq/a:free": () => new Response("no credits", { status: 402 }),
    "cerebras/b:free": textStream("ok"),
    "other/c:free": textStream("ok"),
  });
  await finished;

  const rounds = events.filter((e) => e.type === "thinking_delta" && e.delta.startsWith("\nRound")).length;
  assert.equal(rounds, 1, "no retry after a fatal error");
  const error = events.find((e) => e.type === "error");
  assert.ok(error.error.toLowerCase().includes("insufficient credits"));
  assert.equal(events.find((e) => e.type === "done"), undefined);
});

test("raceWidth=1 serializes candidates", async () => {
  const { events, finished } = setup(
    {
      "groq/a:free": () => new Response("x", { status: 429 }),
      "cerebras/b:free": textStream("serial"),
    },
    { config: { raceWidth: 1, maxBatches: 2 } },
  );
  await finished;

  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Round 1: groq/a:free")));
  assert.ok(events.some((e) => e.type === "thinking_delta" && e.delta.includes("Round 2: cerebras/b:free")));
  assert.ok(events.some((e) => e.type === "done" && e.message.content[1].text === "serial"));
});

test("provider.stream.cancel aborts the whole run", async () => {
  const signal = new AbortController();
  const { events, finished } = setup(
    { "groq/a:free": () => hangForever(), "cerebras/b:free": () => hangForever() },
    { signal: signal.signal },
  );
  setTimeout(() => signal.abort(), 30);
  await finished;

  const error = events.find((e) => e.type === "error");
  assert.equal(error.reason, "aborted");
  assert.equal(error.error, "Request was cancelled.");
});

test("a clean two-chunk stream finishes with done (control case)", async () => {
  const { events, finished } = setup({
    "groq/a:free": textStream("first"),
    "cerebras/b:free": () => hangForever(),
  });
  await finished;
  assert.ok(events.some((e) => e.type === "done"));
});

test("stalled winner (open body, silent after first token) reports a stall error", async () => {
  // Body emits one chunk then never closes and never enqueues again.
  const stallAfterFirst = () => {
    const encoder = new TextEncoder();
    return new Response(
      new ReadableStream({
        start(controller) {
          controller.enqueue(encoder.encode(`data: ${JSON.stringify(chatChunk({ content: "first" }))}\n\n`));
        },
      }),
      { status: 200 },
    );
  };
  const { events, finished } = setup({
    "groq/a:free": stallAfterFirst,
    "cerebras/b:free": () => hangForever(),
  }, { config: { idleTimeoutMs: 100 } });
  await finished;

  const error = events.find((e) => e.type === "error");
  assert.ok(error.error.includes("stalled"));
  assert.equal(events.find((e) => e.type === "done"), undefined);
});

test("missing API key fails fast with a configuration error", async () => {
  const router = new FreeRouter(IDS, {});
  const events = [];
  await runFreeRouterStream({
    request: BASE_REQUEST,
    config: { ...DEFAULTS },
    pool: { ensure: async () => ({ models: MODELS, router }) },
    apiKey: "",
    emit: (e) => events.push(e),
  });
  const error = events.find((e) => e.type === "error");
  assert.ok(error.error.includes("No OpenRouter API key"));
});

test("tool-call-only winner streams toolcall events", async () => {
  const { events, finished } = setup({
    "groq/a:free": () =>
      sseResponse([
        chatChunk({ toolCalls: [{ index: 0, id: "t1", function: { name: "read", arguments: '{"p":1}' } }], finishReason: "tool_calls" }),
        "[DONE]",
      ]),
    "cerebras/b:free": () => hangForever(),
  });
  await finished;

  const start = events.find((e) => e.type === "toolcall_start");
  assert.equal(start.contentIndex, 1);
  assert.equal(start.toolName, "read");
  const end = events.find((e) => e.type === "toolcall_end");
  assert.deepEqual(end.toolCall.arguments, { p: 1 });
  const done = events.find((e) => e.type === "done");
  assert.equal(done.message.stopReason, "toolUse");
  assert.equal(done.reason, "toolUse");
});
