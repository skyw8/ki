import { configuredApiKey } from "../config.js";
import { compactText, splitDomainFilter } from "../normalize.js";

const SEARCH_URL = "https://api.search.tinyfish.ai";
const FETCH_URL = "https://api.fetch.tinyfish.ai";

function timeoutSignal(signal, ms) {
  const timeout = AbortSignal.timeout(ms);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

function keyFor(config) {
  return configuredApiKey(config, "tinyfishApiKey", "TINYFISH_API_KEY");
}

export function tinyfishAvailable(config) {
  return Boolean(keyFor(config));
}

function searchUrl(query, options) {
  const params = new URLSearchParams({ query });
  const { include, exclude } = splitDomainFilter(options.domainFilter);
  if (include.length) params.set("include_domains", include.join(","));
  if (exclude.length) params.set("exclude_domains", exclude.join(","));
  const recency = { day: 1_440, week: 10_080, month: 43_200, year: 525_600 }[options.recencyFilter];
  if (recency) params.set("recency_minutes", String(recency));
  return `${SEARCH_URL}?${params}`;
}

async function jsonRequest(url, key, init, signal, ms, label) {
  const response = await fetch(url, {
    ...init,
    headers: { "X-API-Key": key, ...(init.body ? { "Content-Type": "application/json" } : {}), ...init.headers },
    signal: timeoutSignal(signal, ms),
  });
  const raw = await response.text();
  if (!response.ok) throw new Error(`tinyfish-${label}-http-${response.status}: ${compactText(raw, 260)}`);
  try { return JSON.parse(raw); } catch { throw new Error(`tinyfish-${label}-invalid-response: response was not JSON`); }
}

export async function searchTinyfish(query, options, config, signal) {
  const key = keyFor(config);
  if (!key) throw new Error("tinyfish-auth-missing: tinyfishApiKey is not configured");
  const data = await jsonRequest(searchUrl(query, options), key, { method: "GET" }, signal, 60_000, "search");
  if (!Array.isArray(data.results)) throw new Error("tinyfish-search-invalid-response: results is not an array");
  const results = data.results.filter((item) => typeof item?.url === "string" && item.url.trim()).slice(0, options.numResults).map((item) => ({
    title: typeof item.title === "string" && item.title.trim() ? item.title.trim() : item.url,
    url: item.url.trim(),
    snippet: compactText(item.snippet, 600),
    publishedAt: item.date || undefined,
    provider: "tinyfish",
  }));
  let inlineContent = [];
  if (options.includeContent && results.length) inlineContent = await fetchTinyfishMany(results.map((item) => item.url), key, signal);
  return { answer: results.map((item) => `${item.snippet}\nSource: ${item.title} (${item.url})`).join("\n\n"), results, inlineContent, provider: "tinyfish" };
}

export async function fetchTinyfish(urls, config, signal) {
  const key = keyFor(config);
  if (!key) throw new Error("tinyfish-auth-missing: tinyfishApiKey is not configured");
  const data = await jsonRequest(FETCH_URL, key, {
    method: "POST",
    body: JSON.stringify({ urls, format: "markdown", per_url_timeout_ms: 30_000 }),
  }, signal, 150_000, "fetch");
  if (!Array.isArray(data.results)) throw new Error("tinyfish-fetch-invalid-response: results is not an array");
  return data.results.flatMap((item) => {
    const content = typeof item?.text === "string" ? item.text.trim() : item?.text ? JSON.stringify(item.text) : "";
    if (!content) return [];
    return [{ url: item.url || item.final_url, title: item.title || "", content, provider: "tinyfish" }];
  });
}

async function fetchTinyfishMany(urls, key, signal) {
  const data = await jsonRequest(FETCH_URL, key, { method: "POST", body: JSON.stringify({ urls, format: "markdown", per_url_timeout_ms: 30_000 }) }, signal, 150_000, "fetch");
  if (!Array.isArray(data.results)) return [];
  return data.results.flatMap((item) => {
    const content = typeof item?.text === "string" ? item.text.trim() : "";
    return content ? [{ url: item.url || item.final_url, title: item.title || "", content, provider: "tinyfish" }] : [];
  });
}
