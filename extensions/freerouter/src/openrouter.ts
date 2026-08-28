// @ts-nocheck
// OpenRouter chat-completions protocol layer.
//
// Converts ki's loop.Request IR (system + types.Message[]) to OpenRouter's
// OpenAI-compatible wire format, streams SSE, and emits compact events that
// match ki's provider.stream.event vocabulary (no partial snapshots — the
// host adapter rebuilds the assistant message from deltas).

export class ModelExhaustedError extends Error {
  /** Quota or request-rejection failure; the router skips the model for a cooldown. */
  constructor(modelId, status) {
    super(`Model ${modelId} quota exceeded (HTTP ${status})`);
    this.name = "ModelExhaustedError";
    this.modelId = modelId;
    this.status = status;
  }
}

export class ModelFatalError extends Error {
  /** Not retriable across models (e.g. HTTP 402 insufficient credits). */
  constructor(message) {
    super(message);
    this.name = "ModelFatalError";
  }
}

function messageText(message) {
  return (message.content ?? [])
    .filter((c) => c.type === "text" || c.type === "")
    .map((c) => c.text)
    .join("");
}

function validObjectArgumentsRaw(raw) {
  if (!raw) return "";
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? raw : "";
  } catch {
    return "";
  }
}

/**
 * ki IR → OpenRouter messages. Mirrors pkg/llmprotocol's completions mapping:
 * consecutive toolResults stay contiguous as role:"tool" items, assistant
 * toolCalls become a top-level tool_calls array, thinking becomes
 * reasoning_content, user images become data-URL image_url parts.
 */
export function toOpenRouterMessages(request) {
  const out = [];
  if (request.system) out.push({ role: "system", content: request.system });

  for (const m of request.messages ?? []) {
    if (m.role === "toolResult") {
      // Images in tool results are dropped (text placeholder): free models
      // with vision are filtered out during discovery anyway.
      const text = messageText(m) || ((m.content ?? []).some((c) => c.type === "image") ? "(see attached image)" : "");
      out.push({ role: "tool", tool_call_id: m.toolCallId ?? "", content: text });
      continue;
    }
    if (m.role === "assistant") {
      const entry = { role: "assistant" };
      let text = "";
      const calls = [];
      for (const c of m.content ?? []) {
        if (c.type === "text" || c.type === "") text += c.text;
        else if (c.type === "thinking") {
          if (c.thinking) entry.reasoning_content = c.thinking;
        } else if (c.type === "toolCall") {
          const args =
            validObjectArgumentsRaw(c.argumentsRaw) ||
            JSON.stringify(c.arguments && typeof c.arguments === "object" ? c.arguments : {});
          calls.push({
            id: c.id,
            type: "function",
            function: { name: c.name, arguments: args },
          });
        }
      }
      if (text) entry.content = text;
      if (calls.length > 0) entry.tool_calls = calls;
      out.push(entry);
      continue;
    }
    // user (default)
    const parts = [];
    let hasMedia = false;
    for (const c of m.content ?? []) {
      if (c.type === "image" && c.data) {
        hasMedia = true;
        parts.push({ type: "image_url", image_url: { url: `data:${c.mimeType || "image/png"};base64,${c.data}` } });
      } else if ((c.type === "text" || c.type === "") && c.text) {
        parts.push({ type: "text", text: c.text });
      }
    }
    // Plain-string content unless an image forces multipart parts.
    if (hasMedia) out.push({ role: "user", content: parts });
    else out.push({ role: "user", content: messageText(m) });
  }
  return out;
}

export function buildChatBody(request, modelId) {
  const body = {
    model: modelId,
    stream: true,
    messages: toOpenRouterMessages(request),
  };
  if (request.maxTokens > 0) body.max_tokens = request.maxTokens;
  // Tools are forwarded as-is: models without function-calling support reject
  // the request with 400 and the router temporarily skips them.
  if ((request.tools ?? []).length > 0) {
    body.tools = request.tools.map((t) => ({
      type: "function",
      function: { name: t.name, description: t.description, parameters: t.parameters },
    }));
  }
  return body;
}

export function normalizeStopReason(finishReason) {
  if (finishReason === "tool_calls") return "toolUse";
  if (finishReason === "length") return "length";
  return "stop";
}

export function newAssistantMessage(modelId) {
  return {
    role: "assistant",
    api: "freerouter",
    provider: "free-router",
    model: modelId,
    content: [],
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0 },
    stopReason: "stop",
    timestamp: Date.now(),
  };
}

/**
 * Stream one free model into an EventQueue.
 *
 * Event contract (indices are 0-based over this model's own content blocks):
 *   start → (text_start/text_delta/text_end | toolcall_start/toolcall_delta/toolcall_end)*
 *         → done{message} | error{reason,error}
 * Terminal errors are also thrown so the racer's floating catch can record
 * cooldowns even after it stopped consuming the queue.
 *
 * @returns {Promise<object>} the final assistant message
 */
export async function streamFreeModel({ modelId, request, apiKey, baseUrl, signal, queue, fetchImpl = fetch }) {
  let response;
  try {
    response = await fetchImpl(`${baseUrl}/chat/completions`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json",
        "X-Title": "freerouter",
      },
      body: JSON.stringify(buildChatBody(request, modelId)),
      signal,
    });
  } catch (err) {
    if (err?.name === "AbortError") {
      queue.push({ type: "error", reason: "aborted", error: `${modelId} aborted` });
      queue.close();
    }
    throw err;
  }

  if (response.status === 402) {
    queue.push({ type: "error", reason: "error", error: "OpenRouter API key has insufficient credits." });
    queue.close();
    throw new ModelFatalError("OpenRouter API key has insufficient credits. Add credits at openrouter.ai/credits.");
  }
  if (response.status === 429 || response.status >= 500 || response.status === 400 || response.status === 422) {
    queue.push({ type: "error", reason: "error", error: `${modelId} failed (HTTP ${response.status})` });
    queue.close();
    throw new ModelExhaustedError(modelId, response.status);
  }
  if (!response.ok) {
    const message = `OpenRouter error: ${response.status} ${response.statusText}`;
    queue.push({ type: "error", reason: "error", error: message });
    queue.close();
    throw new Error(message);
  }
  if (!response.body) {
    queue.push({ type: "error", reason: "error", error: `OpenRouter returned empty body for ${modelId}` });
    queue.close();
    throw new Error(`OpenRouter returned empty body for model ${modelId}`);
  }

  const output = newAssistantMessage(modelId);
  const push = (event) => queue.push(event);
  push({ type: "start" });

  let textStarted = false;
  const pendingToolCalls = new Map(); // OpenRouter tool-call index → accumulator

  const closeText = () => {
    if (!textStarted) return;
    const block = output.content[0];
    push({ type: "text_end", contentIndex: 0, content: block?.text ?? "" });
    textStarted = false;
  };

  const flushToolCalls = () => {
    for (const [, p] of pendingToolCalls) {
      let args = {};
      try {
        args = JSON.parse(p.argsBuffer);
      } catch {
        args = { _raw: p.argsBuffer };
      }
      const block = output.content[p.contentIndex];
      if (block?.type === "toolCall") block.arguments = args;
      push({
        type: "toolcall_end",
        contentIndex: p.contentIndex,
        toolCallId: p.id,
        toolName: p.name,
        toolCall: { type: "toolCall", id: p.id, name: p.name, arguments: args },
      });
    }
    pendingToolCalls.clear();
  };

  const fail = (err) => {
    // Close open blocks so the host never sees a dangling *_start.
    closeText();
    flushToolCalls();
    const isAbort = err instanceof Error && err.name === "AbortError";
    push({ type: "error", reason: isAbort ? "aborted" : "error", error: isAbort ? `${modelId} aborted` : String(err?.message ?? err) });
    queue.close();
  };

  try {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    const handleChunk = (chunk) => {
      // OpenRouter sometimes delivers errors inline as HTTP 200 + error chunk
      // instead of a non-200 status; classify identically to HTTP failures.
      if (chunk.error) {
        const code = Number(chunk.error.code ?? chunk.error.status ?? 0);
        if (code === 402) throw new ModelFatalError(chunk.error.message ?? "insufficient credits");
        throw new ModelExhaustedError(modelId, code || 400);
      }
      const choice = chunk.choices?.[0];
      const delta = choice?.delta;
      if (chunk.usage) {
        output.usage.input = chunk.usage.prompt_tokens ?? 0;
        output.usage.output = chunk.usage.completion_tokens ?? 0;
        output.usage.totalTokens = chunk.usage.total_tokens ?? 0;
      }
      if (choice?.finish_reason) output.stopReason = normalizeStopReason(choice.finish_reason);

      if (delta?.content) {
        if (!textStarted) {
          output.content.push({ type: "text", text: "" });
          push({ type: "text_start", contentIndex: 0 });
          textStarted = true;
        }
        output.content[0].text += delta.content;
        push({ type: "text_delta", contentIndex: 0, delta: delta.content });
      }

      if (delta?.tool_calls) {
        for (const tc of delta.tool_calls) {
          const tcIdx = tc.index ?? 0;
          if (tc.id) {
            const contentIndex = output.content.length;
            const name = tc.function?.name ?? "";
            output.content.push({ type: "toolCall", id: tc.id, name, arguments: {} });
            pendingToolCalls.set(tcIdx, { contentIndex, id: tc.id, name, argsBuffer: tc.function?.arguments ?? "" });
            push({
              type: "toolcall_start",
              contentIndex,
              toolCallId: tc.id,
              toolName: name,
              toolCall: { type: "toolCall", id: tc.id, name },
            });
          } else if (tc.function?.arguments) {
            const p = pendingToolCalls.get(tcIdx);
            if (p) {
              p.argsBuffer += tc.function.arguments;
              push({
                type: "toolcall_delta",
                contentIndex: p.contentIndex,
                delta: tc.function.arguments,
                toolCallId: p.id,
              });
            }
          }
        }
      }
    };

    const finish = () => {
      closeText();
      flushToolCalls();
      push({ type: "done", reason: output.stopReason, message: output });
      queue.close();
    };

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const line of lines) {
        if (!line.startsWith("data: ")) continue;
        const data = line.slice(6).trim();
        if (data === "[DONE]") {
          finish();
          return output;
        }
        let chunk;
        try {
          chunk = JSON.parse(data);
        } catch {
          continue;
        }
        handleChunk(chunk);
      }
    }
    // Flush any remaining decoded bytes and terminate without [DONE].
    buffer += decoder.decode();
    if (buffer.startsWith("data: ")) {
      try {
        handleChunk(JSON.parse(buffer.slice(6).trim()));
      } catch {
        /* trailing garbage — ignore */
      }
    }
    finish();
    return output;
  } catch (err) {
    fail(err);
    throw err;
  }
}
