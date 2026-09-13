import { fetchContents } from "./content.js";
import { responseId } from "./cache.js";
import { settleWithGrace, LONG_PROVIDERS, providerSearchBudget, TIMEOUTS } from "./deadlines.js";
import { canonicalUrl, compactText, hostOf, tokenSimilarity } from "./normalize.js";
import { enabledProviders, providerAvailability, searchProvider, PROVIDERS, fetchTinyfish } from "./providers/index.js";

const PROVIDER_WEIGHT = { codex: 1.15, exa: 1.08, tinyfish: 1, duckduckgo: 0.88 };

function resultKey(item) {
  return canonicalUrl(item.url) || item.url;
}

function nearDuplicate(left, right) {
  if (hostOf(left.url) && hostOf(left.url) === hostOf(right.url) && tokenSimilarity(left.title, right.title) >= 0.86) return true;
  return tokenSimilarity(`${left.title} ${left.snippet}`, `${right.title} ${right.snippet}`) >= 0.93;
}

function mergeResults(runs, limit) {
  const merged = new Map();
  for (const run of runs) {
    for (let index = 0; index < (run.results || []).length; index++) {
      const raw = run.results[index];
      if (!raw || typeof raw.url !== "string") continue;
      const url = canonicalUrl(raw.url);
      if (!url) continue;
      const rank = index + 1;
      const existing = merged.get(url);
      const candidate = {
        ...raw,
        url,
        title: compactText(raw.title || url, 240),
        snippet: compactText(raw.snippet, 800),
        providers: [...new Set([...(existing?.providers || []), run.provider])],
        ranks: { ...(existing?.ranks || {}), [run.provider]: rank },
        score: (existing?.score || 0) + (PROVIDER_WEIGHT[run.provider] || 1) / (60 + rank),
        content: raw.content || existing?.content || "",
        contentError: raw.contentError || existing?.contentError || "",
      };
      if (!existing || candidate.score > existing.score) merged.set(url, candidate);
      else merged.set(url, { ...existing, providers: candidate.providers, ranks: candidate.ranks, score: candidate.score, snippet: existing.snippet || candidate.snippet, content: existing.content || candidate.content, contentError: existing.contentError || candidate.contentError });
    }
  }
  const sorted = [...merged.values()].sort((a, b) => b.score - a.score || a.url.localeCompare(b.url));
  const chosen = [];
  const domainCount = new Map();
  for (const item of sorted) {
    if (chosen.some((existing) => nearDuplicate(existing, item))) continue;
    const domain = hostOf(item.url);
    const count = domainCount.get(domain) || 0;
    // Give a multi-provider search meaningful domain diversity while still
    // allowing a single provider to return several pages from one site.
    const domainCap = runs.length > 1 ? Math.max(2, Math.ceil(limit / 2)) : limit;
    if (count >= domainCap) continue;
    domainCount.set(domain, count + 1);
    chosen.push(item);
    if (chosen.length >= limit) break;
  }
  return chosen;
}

function providerError(provider, error, durationMs) {
  const message = error instanceof Error ? error.message : String(error);
  const lower = message.toLowerCase();
  const status = message.match(/(?:http-|HTTP |status )([45]\d\d)/i)?.[1];
  // Distinguish "this provider ran out of budget" from a real provider fault so
  // the model can retry a straggler instead of treating it as a hard failure.
  const timedOut = lower.includes("timeout") || lower.includes("budget") || lower.includes("abort");
  return {
    provider,
    ok: false,
    durationMs,
    error: compactText(message, 320),
    category: status === "401" ? "auth" : status === "429" ? "rate-limit" : timedOut ? "timeout" : lower.includes("network") || lower.includes("fetch") ? "network" : "provider",
  };
}

// forwardAbort mirrors a parent (host cancel or query budget) onto a fresh
// controller so per-provider and content requests can be cut independently.
function forwardAbort(parent: AbortSignal | undefined): AbortController {
  const controller = new AbortController();
  if (!parent) return controller;
  const forward = () => controller.abort(parent.reason ?? new Error("aborted"));
  if (parent.aborted) forward();
  else parent.addEventListener("abort", forward, { once: true });
  return controller;
}

// hydrateContent runs the content pass under its own budget. Direct fetches and
// the TinyFish fallback share this deadline, so a stalled fetch cannot extend
// the search result the caller is waiting on.
export async function hydrateContent(results, { signal, fallback }: { signal?: AbortSignal; fallback?: (urls: string[], signal?: AbortSignal) => Promise<any[]> } = {}) {
  const controller = forwardAbort(signal);
  const timer = setTimeout(() => controller.abort(new Error("content-budget-exceeded")), TIMEOUTS.providerContent);
  try {
    return await fetchContents(results, { signal: controller.signal, fallback });
  } finally {
    clearTimeout(timer);
  }
}

export async function aggregateSearch(query, options, config, signal, onProgress: (partial: any) => void = () => {}) {
  const selected = enabledProviders(config, options.provider);
  if (!selected.length) throw new Error("provider-config-error: all selected providers are disabled");
  if (options.proxy) {
    // The public schema retains proxy for parity with pi-web-access. Native
    // fetch has no portable dispatcher, so never silently pretend it worked.
    throw new Error("proxy-unsupported: this sidecar currently supports direct HTTP(S) only");
  }
  const runs = [];
  const diagnostics = [];
  const active = [];
  for (const provider of selected) {
    if (!providerAvailability(provider, config)) {
      diagnostics.push({ provider, ok: false, category: "unavailable", error: provider === "codex" ? "codex-auth-missing" : "provider credentials are not configured" });
      continue;
    }
    active.push(provider);
  }
  if (!active.length) throw new Error("provider-config-error: no enabled provider has usable credentials");
  // Providers race their own budgets. A straggler is cut a short grace after a
  // quorum of providers is in, so one slow index cannot hold the query until
  // the host tool deadline and discard the results that already succeeded.
  // A long-budget provider (Codex) is exempt: when it participates the query
  // waits for it up to its own budget instead of cutting it at the grace.
  const longActive = active.some((provider) => LONG_PROVIDERS.includes(provider));
  const searchController = forwardAbort(signal);
  const searchTimer = setTimeout(() => searchController.abort(new Error("query-budget-exceeded")), TIMEOUTS.query);
  const entries = active.map((provider) => {
    const child = new AbortController();
    const onAbort = () => child.abort(searchController.signal.reason);
    if (searchController.signal.aborted) onAbort();
    else searchController.signal.addEventListener("abort", onAbort, { once: true });
    const startedAt = Date.now();
    const budget = providerSearchBudget(provider);
    onProgress({ phase: "provider", provider, status: "running" });
    const providerTimer = setTimeout(() => child.abort(new Error("provider-budget-exceeded")), budget);
    const promise = searchProvider(provider, query, options, config, child.signal)
      .then((result) => {
        clearTimeout(providerTimer);
        const durationMs = Date.now() - startedAt;
        onProgress({ phase: "provider", provider, status: "done", count: result.results?.length || 0, durationMs });
        return { provider, result, durationMs, ok: true as const };
      })
      .catch((error) => {
        clearTimeout(providerTimer);
        const durationMs = Date.now() - startedAt;
        onProgress({ phase: "provider", provider, status: "failed", error: compactText(error instanceof Error ? error.message : String(error), 320), durationMs });
        return { provider, error, durationMs, ok: false as const };
      });
    return { provider, promise, budget, abort: () => { clearTimeout(providerTimer); child.abort(new Error("straggler-cut")); } };
  });
  let settled;
  try {
    settled = await settleWithGrace(entries.map((entry) => entry.promise), {
      quorum: longActive ? entries.length : Math.min(2, entries.length),
      graceMs: TIMEOUTS.providerGrace,
      onGrace: () => entries.forEach((entry) => entry.abort()),
    });
  } finally {
    clearTimeout(searchTimer);
  }
  settled.forEach((item, index) => {
    if (!item) {
      diagnostics.push({ provider: entries[index].provider, ok: false, category: "timeout", error: "provider-budget-exceeded", durationMs: entries[index].budget });
      return;
    }
    const value = item.status === "fulfilled" ? item.value : { provider: entries[index].provider, ok: false as const, error: item.reason };
    if (value.ok) {
      runs.push({ ...value.result, provider: value.provider, durationMs: value.durationMs });
      diagnostics.push({ provider: value.provider, ok: true, durationMs: value.durationMs, transport: (value.result as any).transport, count: value.result.results?.length || 0 });
    } else {
      diagnostics.push(providerError(value.provider, value.error, value.durationMs ?? 0));
    }
  });
  if (!runs.length) throw new Error(`provider-failed: ${diagnostics.map((item) => `${item.provider}: ${item.error || "no results"}`).join("; ")}`);
  let results = mergeResults(runs, options.numResults);
  const inline = runs.flatMap((run) => run.inlineContent || []);
  const inlineByUrl = new Map(inline.map((item) => [resultKey(item), item]));
  results = results.map((item) => {
    const content = inlineByUrl.get(resultKey(item));
    return content ? { ...item, content: content.content, fetchedBy: content.provider } : item;
  });
  // deep_web_search defers content (deferContent) so the source pack returns as
  // soon as search settles; source_check keeps it in-band but still bounded.
  if (options.includeContent && !options.deferContent && results.length) {
    const tinyfishFallback = config.providerToggles?.tinyfish !== false && providerAvailability("tinyfish", config)
      ? (urls, fetchSignal) => fetchTinyfish(urls, config, fetchSignal)
      : undefined;
    results = await hydrateContent(results, { signal, fallback: tinyfishFallback });
  }
  const id = responseId(query, options);
  const answer = runs.map((run) => run.answer).filter(Boolean).join("\n\n");
  return { responseId: id, query, results, answer, runs, diagnostics };
}

export function combineAggregates(query, aggregates, options) {
  const allRuns = aggregates.flatMap((item) => item.runs || []);
  const merged = mergeResults(allRuns, options.numResults);
  const byUrl = new Map(aggregates.flatMap((item) => item.results || []).map((item) => [resultKey(item), item]));
  return {
    responseId: responseId(query, options),
    query,
    results: merged.map((item) => ({ ...item, ...((byUrl.get(resultKey(item)) as any) || {}) })),
    answer: aggregates.map((item) => item.answer).filter(Boolean).join("\n\n"),
    runs: allRuns,
    diagnostics: aggregates.flatMap((item) => item.diagnostics || []),
  };
}

export function sourcePackText(result, { includeContent = false, maxChars = 12_000 } = {}) {
  const lines = [`Search results for: ${result.query}`, `Sources: ${result.results.length}`];
  for (let index = 0; index < result.results.length; index++) {
    const item = result.results[index];
    lines.push(`${index + 1}. ${item.title} — ${item.url}`);
    if (item.snippet) lines.push(`   ${compactText(item.snippet, 500)}`);
    if (includeContent && item.content) lines.push(`   Content: ${compactText(item.content, 1_000)}`);
    if (item.providers?.length) lines.push(`   Providers: ${item.providers.join(", ")}`);
  }
  if (result.answer) lines.push(`\nProvider notes:\n${compactText(result.answer, 2_500)}`);
  const failed = (result.diagnostics || []).filter((item) => !item.ok);
  if (failed.length) lines.push(`\nProvider diagnostics:\n${failed.map((item) => `- ${item.provider}: ${item.error || item.category}`).join("\n")}`);
  return lines.join("\n").slice(0, maxChars);
}

export function providerNames() { return PROVIDERS.slice(); }
