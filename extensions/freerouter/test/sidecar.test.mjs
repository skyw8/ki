import { test } from "node:test";
import assert from "node:assert/strict";
import { createSidecar } from "../dist/index.js";

test("request handlers respond with their returned value", async () => {
  const sidecar = createSidecar({ handlers: { initialize: () => ({ tools: [] }) } });
  const responses = [];
  const originalWrite = process.stdout.write.bind(process.stdout);
  process.stdout.write = (chunk) => {
    responses.push(JSON.parse(chunk));
    return true;
  };
  try {
    await sidecar.dispatch(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }));
  } finally {
    process.stdout.write = originalWrite;
  }
  assert.deepEqual(responses, [{ jsonrpc: "2.0", id: 1, result: { tools: [] } }]);
});

test("unknown methods answer -32601 when they carry an id", async () => {
  const sidecar = createSidecar({ handlers: {} });
  const responses = [];
  const originalWrite = process.stdout.write.bind(process.stdout);
  process.stdout.write = (chunk) => {
    responses.push(JSON.parse(chunk));
    return true;
  };
  try {
    await sidecar.dispatch(JSON.stringify({ jsonrpc: "2.0", id: "x", method: "nope" }));
    await sidecar.dispatch(JSON.stringify({ jsonrpc: "2.0", method: "nope" })); // notification: silent
  } finally {
    process.stdout.write = originalWrite;
  }
  assert.equal(responses.length, 1);
  assert.equal(responses[0].error.code, -32601);
});

test("provider.stream.start acks synchronously then streams events and is cancellable", async () => {
  const responses = [];
  const originalWrite = process.stdout.write.bind(process.stdout);
  process.stdout.write = (chunk) => {
    responses.push(JSON.parse(chunk));
    return true;
  };
  try {
    const done = new Promise((resolve) => {
      responses.onAbort = resolve;
    });
    const sidecar2 = createSidecar({
      handlers: {
        "provider.stream.start": (params, ctx) => {
          ctx.respond({ accepted: true });
          const signal = ctx.cancelSignal(params.requestId);
          signal.addEventListener("abort", () => {
            ctx.notify("provider.stream.event", { requestId: params.requestId, type: "error", reason: "aborted" });
            responses.onAbort();
          });
        },
        "provider.stream.cancel": ({ requestId }) => sidecar2.cancel(requestId),
      },
    });
    await sidecar2.dispatch(
      JSON.stringify({ jsonrpc: "2.0", id: 7, method: "provider.stream.start", params: { requestId: "r1" } }),
    );
    await sidecar2.dispatch(JSON.stringify({ jsonrpc: "2.0", method: "provider.stream.cancel", params: { requestId: "r1" } }));
    await done;

    const ack = responses.find((r) => r.id === 7);
    assert.deepEqual(ack.result, { accepted: true });
    const event = responses.find((r) => r.method === "provider.stream.event");
    assert.equal(event.params.type, "error");
    assert.equal(event.params.reason, "aborted");
  } finally {
    process.stdout.write = originalWrite;
  }
});
