import { test } from "node:test";
import assert from "node:assert/strict";
import { EventQueue } from "../dist/racer.js";
import {
  streamFreeModel,
  toOpenRouterMessages,
  buildChatBody,
  ModelExhaustedError,
  ModelFatalError,
} from "../dist/openrouter.js";
import { sseResponse, chatChunk } from "./helpers.mjs";

const BASE_REQUEST = {
  system: "sys",
  messages: [],
  tools: [],
  maxTokens: 123,
};

test("toOpenRouterMessages: system, toolResult, assistant toolCalls, thinking", () => {
  const out = toOpenRouterMessages({
    system: "S",
    messages: [
      { role: "user", content: [{ type: "text", text: "hi" }] },
      {
        role: "assistant",
        content: [
          { type: "text", text: "let me check" },
          { type: "thinking", thinking: "hmm" },
          { type: "toolCall", id: "t1", name: "read", argumentsRaw: '{"path":"a"}' },
        ],
      },
      { role: "toolResult", toolCallId: "t1", content: [{ type: "text", text: "file body" }] },
      { role: "toolResult", toolCallId: "t2", content: [{ type: "image", data: "zzz" }] },
      { role: "user", content: [{ type: "image", data: "abc", mimeType: "image/jpeg" }, { type: "text", text: "what" }] },
    ],
  });
  assert.deepEqual(out[0], { role: "system", content: "S" });
  assert.deepEqual(out[1], { role: "user", content: "hi" });
  assert.equal(out[2].content, "let me check");
  assert.equal(out[2].reasoning_content, "hmm");
  assert.deepEqual(out[2].tool_calls, [{ id: "t1", type: "function", function: { name: "read", arguments: '{"path":"a"}' } }]);
  assert.deepEqual(out[3], { role: "tool", tool_call_id: "t1", content: "file body" });
  assert.deepEqual(out[4], { role: "tool", tool_call_id: "t2", content: "(see attached image)" });
  assert.deepEqual(out[5], {
    role: "user",
    content: [
      { type: "image_url", image_url: { url: "data:image/jpeg;base64,abc" } },
      { type: "text", text: "what" },
    ],
  });
});

test("buildChatBody forwards tools and maxTokens", () => {
  const body = buildChatBody(
    {
      system: "s",
      messages: [],
      maxTokens: 500,
      tools: [{ name: "read", description: "d", parameters: { type: "object" } }],
    },
    "m:free",
  );
  assert.equal(body.model, "m:free");
  assert.equal(body.max_tokens, 500);
  assert.deepEqual(body.tools, [{ type: "function", function: { name: "read", description: "d", parameters: { type: "object" } } }]);
  assert.equal(body.stream, true);
});

test("streamFreeModel forwards text deltas and finishes with usage + stopReason", async () => {
  const queue = new EventQueue();
  const fetchImpl = async () =>
    sseResponse([
      chatChunk({ content: "Hel" }),
      chatChunk({ content: "lo", finishReason: "stop", usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } }),
      "[DONE]",
    ]);
  const message = await streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl });
  assert.equal(message.content[0].text, "Hello");
  assert.equal(message.usage.totalTokens, 15);
  assert.equal(message.stopReason, "stop");
  assert.equal(message.model, "m:free");

  const events = [];
  for (let e = queue.next(); ; ) {
    const { value, done } = await e;
    if (done) break;
    events.push(value);
    e = queue.next();
  }
  assert.deepEqual(
    events.map((e) => e.type),
    ["start", "text_start", "text_delta", "text_delta", "text_end", "done"],
  );
  assert.equal(events[2].delta, "Hel");
});

test("streamFreeModel accumulates tool call argument deltas", async () => {
  const queue = new EventQueue();
  const fetchImpl = async () =>
    sseResponse([
      chatChunk({ toolCalls: [{ index: 0, id: "c1", function: { name: "bash", arguments: '{"comm' } }] }),
      chatChunk({ toolCalls: [{ index: 0, function: { arguments: 'and":"ls"}' } }], finishReason: "tool_calls" }),
      "[DONE]",
    ]);
  const message = await streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl });
  assert.equal(message.stopReason, "toolUse");
  assert.deepEqual(message.content[0].arguments, { command: "ls" });
});

test("streamFreeModel: HTTP 429 → ModelExhaustedError and error event", async () => {
  const queue = new EventQueue();
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl: async () => new Response("x", { status: 429 }) }),
    ModelExhaustedError,
  );
  const event = (await queue.next()).value;
  assert.equal(event.type, "error");
});

test("streamFreeModel: HTTP 402 → ModelFatalError", async () => {
  const queue = new EventQueue();
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl: async () => new Response("x", { status: 402 }) }),
    ModelFatalError,
  );
});

test("streamFreeModel: HTTP 400 (e.g. no tool support) → exhausted", async () => {
  const queue = new EventQueue();
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl: async () => new Response("x", { status: 400 }) }),
    ModelExhaustedError,
  );
});

test("streamFreeModel: inline error chunk (HTTP 200) is classified", async () => {
  const queue = new EventQueue();
  const fetchImpl = async () => sseResponse([{ error: { code: 429, message: "rate limited" } }]);
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl }),
    ModelExhaustedError,
  );
});

test("streamFreeModel: inline 402 chunk is fatal", async () => {
  const queue = new EventQueue();
  const fetchImpl = async () => sseResponse([{ error: { code: 402, message: "no credits" } }]);
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl }),
    ModelFatalError,
  );
});

test("streamFreeModel: stream ending without [DONE] still finishes cleanly", async () => {
  const queue = new EventQueue();
  const fetchImpl = async () => sseResponse([chatChunk({ content: "tail" })]);
  const message = await streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, fetchImpl });
  assert.equal(message.content[0].text, "tail");
});

test("streamFreeModel: abort closes the queue with an aborted error", async () => {
  const queue = new EventQueue();
  const controller = new AbortController();
  // Simulate a real fetch: reject with an AbortError once the signal fires.
  const fetchImpl = (url, init) =>
    new Promise((resolve, reject) => {
      if (init.signal.aborted) {
        reject(new DOMException("This operation was aborted", "AbortError"));
        return;
      }
      init.signal.addEventListener("abort", () => reject(new DOMException("This operation was aborted", "AbortError")), { once: true });
    });
  setTimeout(() => controller.abort(), 10);
  await assert.rejects(
    streamFreeModel({ modelId: "m:free", request: BASE_REQUEST, apiKey: "k", baseUrl: "http://x", queue, signal: controller.signal, fetchImpl }),
    /abort/i,
  );
  const event = (await queue.next()).value;
  assert.equal(event.type, "error");
  assert.equal(event.reason, "aborted");
});
