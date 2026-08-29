import { fetchContents } from "./content.js";
import { responseId } from "./cache.js";
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
  const status = message.match(/(?:http-|HTTP |status )([45]\d\d)/i)?.[1];
  return { provider, ok: false, durationMs, error: compactText(message, 320), category: status === "401" ? "auth" : status === "429" ? "rate-limit" : message.toLowerCase().includes("network") || message.toLowerCase().includes("fetch") ? "network" : "provider" };
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
  const settled = await Promise.all(active.map(async (provider) => {
    const startedAt = Date.now();
    onProgress({ phase: "provider", provider, status: "running" });
    try {
      const result = await searchProvider(provider, query, options, config, signal);
      const durationMs = Date.now() - startedAt;
      onProgress({ phase: "provider", provider, status: "done", count: result.results?.length || 0, durationMs });
      return { provider, result, durationMs, ok: true };
    } catch (error) {
      const durationMs = Date.now() - startedAt;
      onProgress({ phase: "provider", provider, status: "failed", error: compactText(error instanceof Error ? error.message : String(error), 320), durationMs });
      return { provider, error, durationMs, ok: false };
    }
  }));
  settled.forEach((item) => {
    if (item.ok) {
      runs.push({ ...item.result, provider: item.provider, durationMs: item.durationMs });
      diagnostics.push({ provider: item.provider, ok: true, durationMs: item.durationMs, transport: (item.result as any).transport, count: item.result.results?.length || 0 });
    } else {
      diagnostics.push(providerError(item.provider, item.error, item.durationMs));
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
  if (options.includeContent && results.length) {
    const tinyfishFallback = config.providerToggles?.tinyfish !== false && providerAvailability("tinyfish", config)
      ? (urls, fetchSignal) => fetchTinyfish(urls, config, fetchSignal)
      : undefined;
    results = await fetchContents(results, { signal, fallback: tinyfishFallback });
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
