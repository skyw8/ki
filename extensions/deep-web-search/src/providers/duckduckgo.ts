import { compactText, normalizeDomain, splitDomainFilter } from "../normalize.js";

const SEARCH_URL = "https://html.duckduckgo.com/html/";

function decodeHtml(value) {
  return value.replace(/&(#x?[0-9a-f]+|amp|lt|gt|quot|apos|nbsp);/gi, (_, entity) => {
    const lower = entity.toLowerCase();
    if (lower === "amp") return "&";
    if (lower === "lt") return "<";
    if (lower === "gt") return ">";
    if (lower === "quot") return '"';
    if (lower === "apos") return "'";
    if (lower === "nbsp") return " ";
    const numeric = lower.startsWith("#x") ? parseInt(lower.slice(2), 16) : lower.startsWith("#") ? parseInt(lower.slice(1), 10) : NaN;
    return Number.isFinite(numeric) ? String.fromCodePoint(numeric) : _;
  });
}

function stripTags(value) {
  return decodeHtml(value.replace(/<[^>]*>/g, " ")).replace(/\s+/g, " ").trim();
}

function destination(value) {
  try {
    const href = new URL(decodeHtml(value), SEARCH_URL);
    const target = href.searchParams.get("uddg") || href.href;
    const url = new URL(target);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch { return null; }
}

function matches(url, options) {
  try {
    const hostname = new URL(url).hostname.toLowerCase();
    const { include, exclude } = splitDomainFilter(options.domainFilter);
    const hostMatches = (domain) => hostname === domain || hostname.endsWith(`.${domain}`);
    if (include.length && !include.some(hostMatches)) return false;
    return !exclude.some(hostMatches);
  } catch { return false; }
}

export function duckduckgoAvailable() { return true; }

export async function searchDuckduckgo(query, options, signal) {
  const url = new URL(SEARCH_URL);
  url.searchParams.set("q", query);
  const response = await fetch(url, {
    headers: { Accept: "text/html", "User-Agent": "Mozilla/5.0 (compatible; ki-deep-web-search/0.1)" },
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(30_000)]) : AbortSignal.timeout(30_000),
  });
  const html = await response.text();
  if (!response.ok) throw new Error(`duckduckgo-http-${response.status}: ${compactText(html, 260)}`);
  const results = [];
  const pattern = /<div[^>]+class=["'][^"']*result[^"']*["'][^>]*>([\s\S]*?)<\/div>\s*(?=<div[^>]+class=["'][^"']*result|$)/gi;
  let match;
  while ((match = pattern.exec(html)) && results.length < options.numResults) {
    const block = match[1];
    if (/result--ad/i.test(block)) continue;
    const anchor = block.match(/<a[^>]+class=["'][^"']*result__a[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>([\s\S]*?)<\/a>/i);
    if (!anchor) continue;
    const resultUrl = destination(anchor[1]);
    if (!resultUrl || !matches(resultUrl, options)) continue;
    const snippet = stripTags(block.match(/class=["'][^"']*result__snippet[^"']*["'][^>]*>([\s\S]*?)<\//i)?.[1] || "");
    results.push({ title: stripTags(anchor[2]) || resultUrl, url: resultUrl, snippet: compactText(snippet, 600), provider: "duckduckgo" });
  }
  if (!results.length) throw new Error("duckduckgo-empty: response contained no parseable results");
  return { answer: results.map((item) => `${item.snippet}\nSource: ${item.title} (${item.url})`).join("\n\n"), results, provider: "duckduckgo" };
}
