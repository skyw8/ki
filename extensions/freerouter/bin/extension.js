// freerouter sidecar entry. Thin NDJSON JSON-RPC 2.0 server:
//   Host → sidecar: initialize, provider.stream.start, provider.stream.cancel,
//                   config.updated, shutdown, cancel
//   sidecar → Host: provider.stream.event notifications
// Provider streams are process-level; one sidecar serves concurrent streams
// from many sessions keyed by requestId.

import { readConfig } from "../dist/config.js";
import { ModelDiscovery } from "../dist/discovery.js";
import { FreeRouter } from "../dist/router.js";
import { runFreeRouterStream } from "../dist/racer.js";
import { createSidecar } from "../dist/index.js";

const root = process.env.KI_EXTENSION_ROOT || process.cwd();
let config = readConfig(root, process.env);
const discovery = new ModelDiscovery(config);
// One shared cooldown registry across all sessions: OpenRouter's free rate
// limits apply per model+account, not per session.
let router = null;

function log(message) {
  process.stderr.write(`[freerouter] ${message}\n`);
}

function resetRouter(models) {
  router = new FreeRouter(models.map((m) => m.id), {
    exhaustedTtlMs: config.exhaustedTtlMs,
    slowTtlMs: config.slowTtlMs,
  });
}

/** Shared model list + cooldown registry, resolved lazily per request. */
const pool = {
  async ensure(apiKey) {
    const models = await discovery.ensure(apiKey);
    router ??= new FreeRouter(models.map((m) => m.id), {
      exhaustedTtlMs: config.exhaustedTtlMs,
      slowTtlMs: config.slowTtlMs,
    });
    return { models, router };
  },
};

function resolveApiKey(request) {
  return (
    request?.credential?.apiKey ||
    config.apiKey ||
    process.env.OPENROUTER_API_KEY ||
    ""
  );
}

const sidecar = createSidecar({
  handlers: {
    initialize: () => ({ tools: [], commands: [], fallback: false, subscriptions: [] }),
    "provider.stream.start": (params, ctx) => {
      const { requestId, request } = params;
      // The host's 10s start timeout only bounds this ack; discovery and
      // racing happen asynchronously and report through stream events.
      ctx.respond({ accepted: true });
      const emit = (event) => ctx.notify("provider.stream.event", { requestId, ...event });
      runFreeRouterStream({
        request: request?.request ?? {},
        config,
        pool,
        apiKey: resolveApiKey(request),
        emit,
        signal: ctx.cancelSignal(requestId),
        fetchImpl: fetch,
        log,
      })
        .catch((err) => {
          log(`stream ${requestId} crashed: ${err?.stack ?? err}`);
          emit({
            type: "error",
            reason: "error",
          error: `freerouter internal error: ${err?.message ?? err}`,
          });
        })
        .finally(() => sidecar.cancel(requestId));
      // undefined return: we responded ourselves.
    },
    "provider.stream.cancel": ({ requestId }) => {
      sidecar.cancel(requestId);
    },
    "config.updated": () => {
      config = readConfig(root, process.env);
      discovery.reconfigure(config);
      // Rebuild the router so changed TTLs apply; the cached model list seeds
      // the next request's registry.
      if (discovery.models) resetRouter(discovery.models);
    },
    shutdown: () => {
      discovery.stopBackgroundRefresh();
      process.exit(0);
    },
  },
});

sidecar.listen(process.stdin);
