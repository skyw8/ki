// @ts-nocheck
// Free-model discovery against OpenRouter's /models endpoint.
//
// The list is fetched lazily on the first prompt routed through the pseudo
// model, then refreshed in the background so long-running servers pick up
// newly available free models and stop wasting retries on removed ones.

const DISCOVERY_TIMEOUT_MS = 10_000;
const DEFAULT_CONTEXT_WINDOW = 128_000;
const DEFAULT_MAX_TOKENS = 4_096;

// Providers known for low latency on the free tier, ordered by preference.
const FAST_PROVIDER_PREFIXES = ["groq/", "cerebras/", "fireworks/", "together/", "mistralai/"];

// Non-general-assistant models are useless for coding sessions and burn
// candidates: moderation/guard models answer safety prompts, -vl/vision
// models answer with image-centric behavior.
const NON_ASSISTANT_MODEL_MARKERS = ["content-safety", "moderation", "guard", "-vl", "/vl", "vision"];

function speedScore(modelId) {
  const lower = modelId.toLowerCase();
  const idx = FAST_PROVIDER_PREFIXES.findIndex((prefix) => lower.startsWith(prefix));
  return idx === -1 ? FAST_PROVIDER_PREFIXES.length : idx;
}

function isGeneralAssistantModel(modelId) {
  const lower = modelId.toLowerCase();
  return !NON_ASSISTANT_MODEL_MARKERS.some((marker) => lower.includes(marker));
}

/**
 * Fetch and sort OpenRouter's free models.
 * @returns {Promise<Array<{id,name,contextLength,maxTokens}>>}
 */
export async function fetchFreeModels({ apiKey, baseUrl, timeoutMs = DISCOVERY_TIMEOUT_MS, fetchImpl = fetch }) {
  if (!apiKey) throw new Error("OpenRouter API key is required");
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  let response;
  try {
    response = await fetchImpl(`${baseUrl}/models`, {
      headers: { Authorization: `Bearer ${apiKey}` },
      signal: controller.signal,
    });
  } finally {
    clearTimeout(timeout);
  }
  if (!response.ok) {
    throw new Error(`Failed to fetch OpenRouter models: ${response.status} ${response.statusText}`);
  }
  const payload = await response.json();
  const models = (payload?.data ?? [])
    .filter((m) => typeof m?.id === "string" && m.id.includes(":free") && isGeneralAssistantModel(m.id))
    .map((m) => ({
      id: m.id,
      name: m.name ?? m.id,
      contextLength: m.context_length ?? DEFAULT_CONTEXT_WINDOW,
      maxTokens: m.top_provider?.max_completion_tokens ?? DEFAULT_MAX_TOKENS,
    }));
  // Static preference order: fast inference providers first; same-tier models
  // sort by ascending context window (empirically faster). The racer consumes
  // this order.
  models.sort((a, b) => {
    const bySpeed = speedScore(a.id) - speedScore(b.id);
    if (bySpeed !== 0) return bySpeed;
    return a.contextLength - b.contextLength;
  });
  if (models.length === 0) throw new Error("No free models found on OpenRouter");
  return models;
}

/**
 * Shared, lazily-populated cache of free models. One instance lives for the
 * whole sidecar process, so cooldown bookkeeping in FreeRouter stays stable
 * across concurrent streams.
 */
export class ModelDiscovery {
  constructor(config, { fetchImpl = fetch } = {}) {
    this.config = config;
    this.fetchImpl = fetchImpl;
    this.models = null;
    this.inflight = null;
    this.refreshTimer = null;
  }

  /** Models, fetching on first use. Concurrent callers share one fetch. */
  async ensure(apiKey) {
    if (this.models && this.models.length > 0) return this.models;
    this.inflight ??= fetchFreeModels({
      apiKey,
      baseUrl: this.config.baseUrl,
      fetchImpl: this.fetchImpl,
    })
      .then((models) => {
        this.models = models;
        return models;
      })
      .finally(() => {
        this.inflight = null;
      });
    return this.inflight;
  }

  startBackgroundRefresh(onError = console.warn) {
    this.stopBackgroundRefresh();
    this.refreshTimer = setInterval(() => {
      const apiKey =
        this.config.apiKey || process.env.OPENROUTER_API_KEY || "";
      if (!apiKey) return;
      fetchFreeModels({ apiKey, baseUrl: this.config.baseUrl, fetchImpl: this.fetchImpl })
        .then((models) => {
          if (models.length > 0) this.models = models;
        })
        .catch((err) => onError("[freerouter] model list refresh failed:", err?.message ?? err));
    }, this.config.refreshIntervalMs);
    // Never keep the sidecar process alive just for the refresh timer.
    this.refreshTimer.unref?.();
  }

  stopBackgroundRefresh() {
    if (this.refreshTimer) {
      clearInterval(this.refreshTimer);
      this.refreshTimer = null;
    }
  }

  /** Apply updated config (baseUrl / refresh interval) after config.updated. */
  reconfigure(config) {
    this.config = config;
    this.startBackgroundRefresh();
  }
}
