// @ts-nocheck
// Minimal NDJSON JSON-RPC 2.0 sidecar framework for ki extensions.
//
// Handler contract:
//   - handler(params, ctx) returning a value → the value is sent as the
//     response result (request).
//   - handler returning undefined → it responded itself via ctx.respond()
//     (used by provider.stream.start, which acks immediately and then streams
//     events asynchronously).
//   - Unknown method with an id → JSON-RPC error -32601.
export function createSidecar({ handlers }) {
  /** @type {Map<string, AbortController>} */
  const cancellations = new Map();

  const write = (obj) => process.stdout.write(JSON.stringify(obj) + "\n");
  const notify = (method, params) => write({ jsonrpc: "2.0", method, params });
  const respond = (id, result) => write({ jsonrpc: "2.0", id, result });
  const respondError = (id, code, message) => write({ jsonrpc: "2.0", id, error: { code, message } });

  async function handleLine(line) {
    if (!line.trim()) return;
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return;
    }
    if (msg.jsonrpc !== "2.0" || typeof msg.method !== "string") return;
    const handler = handlers[msg.method];
    if (!handler) {
      if (msg.id !== undefined && msg.id !== null) {
        respondError(msg.id, -32601, `method not found: ${msg.method}`);
      }
      return;
    }
    const handlerCtx = {
      notify,
      respond: (result) => {
        if (msg.id !== undefined && msg.id !== null) respond(msg.id, result);
      },
      cancelSignal: (requestId) => {
        const controller = new AbortController();
        cancellations.set(requestId, controller);
        return controller.signal;
      },
    };
    const result = await handler(msg.params ?? {}, handlerCtx);
    // A defined result means the handler wants the framework to respond;
    // undefined means it already responded (or the method is a notification).
    if (result !== undefined && msg.id !== undefined && msg.id !== null) {
      respond(msg.id, result);
    }
  }

  return {
    notify,
    cancel(requestId) {
      cancellations.get(requestId)?.abort();
      cancellations.delete(requestId);
    },
    /** Wire stdin to the dispatcher; exits when stdin closes. */
    listen(input) {
      let buffer = "";
      input.setEncoding("utf8");
      input.on("data", (chunk) => {
        buffer += chunk;
        let newline;
        while ((newline = buffer.indexOf("\n")) !== -1) {
          const line = buffer.slice(0, newline);
          buffer = buffer.slice(newline + 1);
          void handleLine(line);
        }
      });
      input.on("end", () => process.exit(0));
      input.on("error", () => process.exit(1));
    },
    /** Test seam: dispatch one raw line without stdio. */
    async dispatch(line) {
      await handleLine(line);
    },
  };
}
