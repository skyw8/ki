// Shared test helpers: SSE Response construction and fake fetch routing.

/** Build an SSE Response whose body streams the given items. */
export function sseResponse(items, { delayMs = 0, status = 200 } = {}) {
  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    async start(controller) {
      for (const item of items) {
        if (delayMs > 0) await new Promise((resolve) => setTimeout(resolve, delayMs));
        controller.enqueue(encoder.encode(typeof item === "string" ? item : `data: ${JSON.stringify(item)}\n\n`));
      }
      controller.close();
    },
  });
  return new Response(stream, { status, headers: { "content-type": "text/event-stream" } });
}

/** Fetch mock that routes by the `model` field of the JSON request body. */
export function fetchByModel(handlers) {
  return async (url, init) => {
    if (url.endsWith("/models")) {
      if (!handlers.models) return new Response(JSON.stringify({ data: [] }), { status: 200 });
      return handlers.models();
    }
    const body = JSON.parse(init.body);
    const handler = handlers[body.model];
    if (!handler) return new Response(JSON.stringify({ error: { message: "no handler" } }), { status: 404 });
    return handler({ body, init });
  };
}

/** One OpenRouter chat chunk. */
export function chatChunk({ content, toolCalls, finishReason, usage, error } = {}) {
  const chunk = {
    choices: [{
      delta: {
        ...(content !== undefined ? { content } : {}),
        ...(toolCalls !== undefined ? { tool_calls: toolCalls } : {}),
      },
    }],
  };
  if (finishReason) chunk.choices[0].finish_reason = finishReason;
  if (usage) chunk.usage = usage;
  if (error) return { error };
  return chunk;
}

export function jsonResponse(payload, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "content-type": "application/json" } });
}
