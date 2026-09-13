import test from "node:test";
import assert from "node:assert/strict";
import { settleWithGrace, timeoutSignal, providerSearchBudget, LONG_PROVIDERS, TIMEOUTS } from "../src/deadlines.ts";

test("codex gets a long budget while HTTP providers stay short", () => {
  assert.ok(providerSearchBudget("codex") >= 60_000, "codex search needs at least 60s");
  assert.ok(providerSearchBudget("exa") < providerSearchBudget("codex"));
  assert.deepEqual([...LONG_PROVIDERS], ["codex"]);
});

test("budgets nest so the sidecar can always finish before its own tool budget", () => {
  assert.ok(TIMEOUTS.query > providerSearchBudget("codex"), "query must outlast the longest provider");
  assert.ok(TIMEOUTS.tool > TIMEOUTS.query, "tool budget must outlast one query");
});

test("settleWithGrace returns every settled task when all finish first", async () => {
  const results = await settleWithGrace([Promise.resolve("a"), Promise.resolve("b")], { quorum: 2, graceMs: 50 });
  assert.deepEqual(results.map((item) => item.value), ["a", "b"]);
});

test("settleWithGrace cuts a straggler after a quorum and a grace window", async () => {
  let aborted = false;
  const straggler = new Promise(() => {});
  const results = await settleWithGrace(
    [Promise.resolve("a"), Promise.resolve("b"), straggler],
    { quorum: 2, graceMs: 30, onGrace: () => { aborted = true; } },
  );
  assert.equal(results[0].value, "a");
  assert.equal(results[1].value, "b");
  assert.equal(results[2], undefined);
  assert.equal(aborted, true);
});

test("settleWithGrace waits for stragglers when the quorum is not met", async () => {
  const slow = new Promise((resolve) => setTimeout(() => resolve("slow"), 40));
  const results = await settleWithGrace([Promise.resolve("fast"), slow], { quorum: 2, graceMs: 5 });
  assert.equal(results[0].value, "fast");
  assert.equal(results[1].value, "slow");
});

test("settleWithGrace propagates rejections instead of hanging", async () => {
  const results = await settleWithGrace([Promise.reject(new Error("boom"))], { quorum: 1, graceMs: 10 });
  assert.equal(results[0].status, "rejected");
  assert.match(results[0].reason.message, /boom/);
});

test("settleWithGrace resolves immediately for an empty batch", async () => {
  assert.deepEqual(await settleWithGrace([], { quorum: 1, graceMs: 10 }), []);
});

test("timeoutSignal bounds a request by its budget", async () => {
  const signal = timeoutSignal(undefined, 10);
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(signal.aborted, true);
});

test("timeoutSignal honours an upstream abort", () => {
  const controller = new AbortController();
  const signal = timeoutSignal(controller.signal, 10_000);
  controller.abort(new Error("host cancel"));
  assert.equal(signal.aborted, true);
});
