import { test } from "node:test";
import assert from "node:assert/strict";
import { fetchFreeModels } from "../dist/discovery.js";
import { jsonResponse } from "./helpers.mjs";

const MODELS = {
  data: [
    { id: "deepseek/deepseek-v3:free", name: "DeepSeek V3", context_length: 64000, top_provider: { max_completion_tokens: 8192 } },
    { id: "groq/llama-3.3-70b:free", name: "Llama 3.3 Groq", context_length: 32000 },
    { id: "meta-llama/llama-vision:free", name: "Vision", context_length: 1000 },
    { id: "openai/moderation:free", name: "Moderation" },
    { id: "qwen/qwen-2.5-guard:free", name: "Guard" },
    { id: "paid/model", name: "Paid" },
  ],
};

async function discover(fetchImpl) {
  return fetchFreeModels({ apiKey: "k", baseUrl: "http://x", fetchImpl });
}

test("filters to free general-assistant models and sorts fast providers first", async () => {
  const models = await discover(() => jsonResponse(MODELS));
  assert.deepEqual(
    models.map((m) => m.id),
    ["groq/llama-3.3-70b:free", "deepseek/deepseek-v3:free"],
  );
});

test("same speed tier sorts by ascending context window", async () => {
  const models = await discover(() =>
    jsonResponse({
      data: [
        { id: "zeta/z:free", context_length: 128000 },
        { id: "alpha/a:free", context_length: 8000 },
        { id: "groq/g:free", context_length: 128000 },
      ],
    }),
  );
  assert.deepEqual(models.map((m) => m.id), ["groq/g:free", "alpha/a:free", "zeta/z:free"]);
});

test("maps context/maxTokens with defaults", async () => {
  const models = await discover(() => jsonResponse(MODELS));
  const groq = models[0];
  assert.equal(groq.contextLength, 32000);
  assert.equal(groq.maxTokens, 4096); // top_provider missing
  const deepseek = models[1];
  assert.equal(deepseek.maxTokens, 8192);
  assert.equal(deepseek.contextLength, 64000);
});

test("non-OK response throws", async () => {
  await assert.rejects(discover(() => jsonResponse({ error: "x" }, 500)), /500/);
});

test("empty free pool throws a distinct error", async () => {
  await assert.rejects(discover(() => jsonResponse({ data: [] })), /No free models/);
});
