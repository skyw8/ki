import { test } from "node:test";
import assert from "node:assert/strict";
import { FreeRouter } from "../dist/router.js";

function makeRouter(models, options = {}) {
  let tick = 0;
  return new FreeRouter(models, {
    exhaustedTtlMs: 1000,
    slowTtlMs: 100,
    now: () => tick++ * 10,
    ...options,
  });
}

test("nextModels returns preference order until exhausted", () => {
  const router = makeRouter(["a", "b", "c"]);
  assert.deepEqual(router.nextModels(10), ["a", "b", "c"]);
  router.markExhausted("b");
  assert.deepEqual(router.nextModels(10), ["a", "c"]);
});

test("cooldown expires and the model rejoins the pool", () => {
  let now = 0;
  const router = new FreeRouter(["a", "b"], { now: () => now, exhaustedTtlMs: 100, slowTtlMs: 10 });
  router.markExhausted("a");
  now = 99;
  assert.deepEqual(router.nextModels(10), ["b"]);
  now = 101;
  assert.deepEqual(router.nextModels(10), ["a", "b"]);
});

test("markSlow uses a short TTL and never downgrades an exhausted entry", () => {
  let now = 0;
  const router = new FreeRouter(["a"], { now: () => now, exhaustedTtlMs: 1000, slowTtlMs: 100 });
  router.markExhausted("a");
  router.markSlow("a"); // must not shorten the 1000ms TTL
  now = 200;
  assert.deepEqual(router.nextModels(10), []); // still cooling down
  now = 1001;
  assert.deepEqual(router.nextModels(10), ["a"]);
});

test("markSlow on a fresh model uses the short TTL", () => {
  let now = 0;
  const router = new FreeRouter(["a"], { now: () => now, exhaustedTtlMs: 1000, slowTtlMs: 100 });
  router.markSlow("a");
  now = 150;
  assert.deepEqual(router.nextModels(10), ["a"]);
});

test("unknown model ids are ignored", () => {
  const router = makeRouter(["a"]);
  router.markExhausted("zzz");
  router.markSlow("zzz");
  assert.deepEqual(router.nextModels(10), ["a"]);
});
