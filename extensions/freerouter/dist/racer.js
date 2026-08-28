// @ts-nocheck
// The racing state machine: per request, take the next `raceWidth` available
// models (default 2), fire them concurrently, and forward the first model
// that starts producing content. Failed models go into TTL cooldown and the
// next batch tries different candidates.
import { streamFreeModel, ModelExhaustedError, ModelFatalError, } from "./openrouter.js";
/** Minimal async event queue shared by producer (SSE reader) and racer. */
export class EventQueue {
    constructor() {
        this.buffer = [];
        this.waiters = [];
        this.closed = false;
    }
    push(event) {
        if (this.closed)
            return;
        const waiter = this.waiters.shift();
        if (waiter)
            waiter({ value: event, done: false });
        else
            this.buffer.push(event);
    }
    close() {
        if (this.closed)
            return;
        this.closed = true;
        while (this.waiters.length > 0)
            this.waiters.shift()({ value: undefined, done: true });
    }
    next() {
        if (this.buffer.length > 0)
            return Promise.resolve({ value: this.buffer.shift(), done: false });
        if (this.closed)
            return Promise.resolve({ value: undefined, done: true });
        return new Promise((resolve) => this.waiters.push(resolve));
    }
}
/** Combine an outer (user cancel) signal with a per-candidate controller. */
export function mergeSignals(parent, local) {
    if (!parent)
        return { signal: local, cleanup: () => { } };
    if (parent.aborted)
        return { signal: parent, cleanup: () => { } };
    if (local.aborted)
        return { signal: local, cleanup: () => { } };
    const combined = new AbortController();
    const onAbort = () => combined.abort();
    parent.addEventListener("abort", onAbort);
    local.addEventListener("abort", onAbort);
    return {
        signal: combined.signal,
        cleanup: () => {
            parent.removeEventListener("abort", onAbort);
            local.removeEventListener("abort", onAbort);
        },
    };
}
/** Adaptive first-token deadline: tight on batch 1, looser as the pool degrades. */
export function batchTimeoutMs(config, batch) {
    const factors = [1, 1.5, 2];
    return Math.round(config.firstTokenTimeoutMs * factors[Math.min(batch - 1, factors.length - 1)]);
}
// Events that count as "this model is producing output". Everything else
// (start, thinking_*) is consumed without qualifying; a leading reasoning
// chunk does not make a model the winner.
const QUALIFYING = new Set(["text_start", "toolcall_start", "done"]);
const TIMEOUT = Symbol("batch-timeout");
const CANCELLED = Symbol("cancelled");
/**
 * Run one full routed request. Emits ki provider.stream.event payloads via
 * `emit` (without requestId — the caller adds it) and returns when the stream
 * reached a terminal event or was cancelled.
 *
 * @param {object} opts
 * @param {object} opts.request ki loop.Request (system/messages/tools/maxTokens)
 * @param {object} opts.config normalized extension config
 * @param {{ensure: (apiKey: string) => Promise<{models: object[], router: import("./router.js").FreeRouter}>}} opts.pool
 *   lazily resolves the shared free-model list and cooldown registry
 * @param {string} opts.apiKey
 * @param {(event: object) => void} opts.emit
 * @param {AbortSignal} [opts.signal] overall cancel signal (provider.stream.cancel)
 */
export async function runFreeRouterStream({ request, config, pool, apiKey, emit, signal, fetchImpl = fetch, log = () => { }, }) {
    if (!apiKey) {
        emit({
            type: "error",
            reason: "error",
            error: "No OpenRouter API key configured. Set it via PUT /v1/providers/free-router/credential, the extension config apiKey, or OPENROUTER_API_KEY.",
        });
        return;
    }
    let models, router;
    try {
        ({ models, router } = await pool.ensure(apiKey));
    }
    catch (err) {
        emit({ type: "error", reason: "error", error: `freerouter: ${err?.message ?? err}` });
        return;
    }
    // Routing progress lives in a thinking block at content index 0; the
    // winner's own content blocks are remapped after it. The same block is
    // echoed into the final message so the transcript stays consistent with
    // what was streamed.
    const OFFSET = 1;
    let thinkingText = "";
    const emitThinking = (delta) => {
        thinkingText += delta;
        emit({ type: "thinking_delta", contentIndex: 0, delta });
    };
    const getThinkingText = () => thinkingText;
    emit({ type: "thinking_start", contentIndex: 0 });
    emitThinking("Searching free models...");
    const emitAborted = () => {
        emit({ type: "error", reason: "aborted", error: "Request was cancelled." });
    };
    const triedThisRequest = new Set();
    for (let batch = 1; batch <= config.maxBatches; batch++) {
        if (signal?.aborted) {
            emitAborted();
            return;
        }
        const available = router.nextModels(models.length);
        const candidates = available.filter((id) => !triedThisRequest.has(id)).slice(0, config.raceWidth);
        if (candidates.length === 0)
            break;
        // Mark before racing: mid-batch aborts must not cause retries of the
        // same model, and with slowTtl < firstTokenTimeout the pool could
        // otherwise recycle candidates into an endless loop.
        candidates.forEach((id) => triedThisRequest.add(id));
        log(`racing: ${candidates.join(", ")}`);
        emitThinking(`\nRound ${batch}: ${candidates.join(", ")}`);
        const outcome = await raceBatch({
            candidates,
            request,
            apiKey,
            baseUrl: config.baseUrl,
            config,
            signal,
            emit,
            offset: OFFSET,
            emitThinking,
            getThinkingText,
            fetchImpl,
            log,
        });
        if (outcome.cancelled) {
            emitAborted();
            return;
        }
        // Long cooldown for quota failures; late loser failures may still append
        // to outcome.exhausted afterwards — those unmarked entries only cost one
        // extra candidate next request, never correctness.
        outcome.exhausted.forEach((id) => router.markExhausted(id));
        if (outcome.fatal) {
            emitThinking(`\n${outcome.fatal.message}`);
            emit({ type: "error", reason: "error", error: outcome.fatal.message });
            return;
        }
        if (outcome.winner)
            return; // raceBatch already emitted done
        if (outcome.stalled || outcome.winnerStreamError)
            return; // terminal error already emitted
        if (outcome.timedOut) {
            // Nobody produced a first token: models are alive but slow.
            candidates.forEach((id) => router.markSlow(id));
        }
        else {
            candidates.forEach((id) => router.markExhausted(id));
        }
    }
    const message = "All free models exhausted. They will recover automatically — please try again in a moment.";
    emitThinking(`\n${message}`);
    emit({ type: "error", reason: "error", error: message });
}
/**
 * Race one batch of candidates.
 *
 * Correctness notes carried over from the original pi-freerouter design:
 * - pending is keyed by candidate index in a Map, never by array position:
 *   re-armed promises change array positions but not candidate identity.
 * - exhausted is snapshotted at return time; floating catches may append late.
 */
async function raceBatch({ candidates, request, apiKey, baseUrl, config, signal, emit, offset, emitThinking, getThinkingText, fetchImpl, log }) {
    const controllers = candidates.map(() => new AbortController());
    const queues = candidates.map(() => new EventQueue());
    const buffers = candidates.map(() => []);
    const exhausted = [];
    let fatal = null;
    const cleanups = [];
    candidates.forEach((modelId, i) => {
        const merged = mergeSignals(signal, controllers[i].signal);
        cleanups.push(merged.cleanup);
        streamFreeModel({
            modelId,
            request,
            apiKey,
            baseUrl,
            signal: merged.signal,
            queue: queues[i],
            fetchImpl,
        }).catch((err) => {
            if (err instanceof ModelExhaustedError)
                exhausted.push(modelId);
            else if (err instanceof ModelFatalError) {
                if (!fatal) {
                    fatal = err;
                    // Surface the credit error immediately instead of waiting out the
                    // remaining candidates.
                    controllers.forEach((c) => c.abort());
                }
            }
            // AbortError and unexpected errors carry no router action.
        });
    });
    let onCancel;
    const cancelledPromise = new Promise((resolve) => {
        if (!signal)
            return;
        onCancel = () => resolve(CANCELLED);
        if (signal.aborted)
            onCancel();
        else
            signal.addEventListener("abort", onCancel, { once: true });
    });
    cleanups.push(() => signal?.removeEventListener("abort", onCancel));
    const remap = (event) => event.contentIndex === undefined ? event : { ...event, contentIndex: event.contentIndex + offset };
    const forwardTerminal = (event) => {
        if (event.type === "done") {
            const message = event.message;
            emit({
                type: "done",
                reason: message?.stopReason ?? event.reason,
                message: {
                    ...message,
                    content: [{ type: "thinking", thinking: getThinkingText().trim() }, ...(message?.content ?? [])],
                },
            });
        }
        else {
            emit(remap(event));
        }
    };
    // Forward the winner's buffered events, then keep piping until a terminal
    // event, an idle stall, or cancellation.
    const finishWithWinner = async (idx, qualifyingEvent) => {
        // Stop the losers first so they stop burning provider quota.
        controllers.forEach((c, j) => {
            if (j !== idx)
                c.abort();
        });
        for (const e of buffers[idx]) {
            if (e.type !== "start")
                emit(remap(e));
        }
        emitThinking(`\nUsing ${candidates[idx]}`);
        forwardTerminal(qualifyingEvent);
        if (qualifyingEvent.type === "done") {
            return { winner: candidates[idx], exhausted, fatal };
        }
        while (true) {
            let idleHandle;
            const step = await Promise.race([
                queues[idx].next().then((result) => ({ kind: "event", result })),
                new Promise((resolve) => {
                    idleHandle = setTimeout(() => resolve({ kind: "idle" }), config.idleTimeoutMs);
                }),
                cancelledPromise.then(() => ({ kind: "cancelled" })),
            ]);
            clearTimeout(idleHandle);
            if (step.kind === "idle") {
                controllers[idx].abort();
                log(`winner ${candidates[idx]} stalled after winning; closing stream`);
                emit({ type: "error", reason: "error", error: `${candidates[idx]} stream stalled` });
                return { winner: candidates[idx], stalled: true, exhausted, fatal };
            }
            if (step.kind === "cancelled") {
                controllers[idx].abort();
                return { winner: candidates[idx], cancelled: true, exhausted, fatal };
            }
            const { value: event, done } = step.result;
            if (done)
                return { winner: candidates[idx], exhausted, fatal };
            if (event.type === "done") {
                forwardTerminal(event);
                return { winner: candidates[idx], exhausted, fatal };
            }
            if (event.type === "error") {
                emit(remap(event));
                return { winner: candidates[idx], winnerStreamError: true, exhausted, fatal };
            }
            emit(remap(event));
        }
    };
    try {
        /** @type {Map<number, Promise<{idx:number, result:object}>>} */
        const pending = new Map();
        const arm = (i) => pending.set(i, queues[i].next().then((result) => ({ idx: i, result })));
        candidates.forEach((_, i) => arm(i));
        let deadlineHandle;
        const deadline = new Promise((resolve) => {
            deadlineHandle = setTimeout(() => resolve(TIMEOUT), batchTimeoutMs(config, 1));
        });
        cleanups.push(() => clearTimeout(deadlineHandle));
        while (pending.size > 0) {
            if (fatal) {
                // A fatal (402-class) failure makes every other candidate pointless.
                controllers.forEach((c) => c.abort());
                return { winner: null, exhausted, fatal };
            }
            const resolved = await Promise.race([deadline, cancelledPromise, ...pending.values()]);
            if (resolved === TIMEOUT) {
                controllers.forEach((c) => c.abort());
                return { winner: null, timedOut: true, exhausted, fatal };
            }
            if (resolved === CANCELLED) {
                controllers.forEach((c) => c.abort());
                return { cancelled: true, exhausted, fatal };
            }
            const { idx, result } = resolved;
            pending.delete(idx);
            if (result.done)
                continue; // candidate stream ended without qualifying
            const event = result.value;
            if (QUALIFYING.has(event.type)) {
                return await finishWithWinner(idx, event);
            }
            if (event.type === "error")
                continue; // stream ends next tick; floating catch records cooldown
            buffers[idx].push(event);
            arm(idx);
        }
        return { winner: null, exhausted, fatal };
    }
    finally {
        cleanups.forEach((cb) => cb());
    }
}
