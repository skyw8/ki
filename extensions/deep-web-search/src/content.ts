import { compactText, canonicalUrl } from "./normalize.js";

const MAX_BODY_BYTES = 5 * 1024 * 1024;
const MAX_REDIRECTS = 5;
const REQUEST_TIMEOUT_MS = 30_000;

function timeoutSignal(signal) {
  const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

function isPrivateHost(hostname) {
  const host = hostname.toLowerCase().replace(/^\[|\]$/g, "");
  if (host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local") || host === "::1") return true;
  if (/^127\./.test(host) || /^10\./.test(host) || /^192\.168\./.test(host) || /^169\.254\./.test(host)) return true;
  const ipv4 = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (ipv4) {
    const [a, b] = ipv4.slice(1, 3).map(Number);
    if (a === 0 || a >= 224 || (a === 172 && b >= 16 && b <= 31)) return true;
  }
  return host === "::" || host.startsWith("fc") || host.startsWith("fd") || host.startsWith("fe80:");
}

export function safeFetchUrl(raw) {
  const url = new URL(raw);
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("content-url-invalid: only HTTP(S) URLs are allowed");
  if (url.username || url.password) throw new Error("content-url-invalid: credentialed URLs are not allowed");
  if (isPrivateHost(url.hostname)) throw new Error("content-url-blocked: local or private hosts are not allowed");
  return url;
}

async function fetchResponse(start, signal) {
  let current = safeFetchUrl(start);
  for (let redirects = 0; redirects <= MAX_REDIRECTS; redirects++) {
    const response = await fetch(current, {
      headers: { Accept: "text/html,application/xhtml+xml,application/json,text/plain;q=0.8", "User-Agent": "ki-deep-web-search/0.1" },
      redirect: "manual",
      signal: timeoutSignal(signal),
    });
    if (![301, 302, 303, 307, 308].includes(response.status)) return { response, url: current.toString() };
    const location = response.headers.get("location");
    if (!location) return { response, url: current.toString() };
    if (redirects === MAX_REDIRECTS) throw new Error("content-redirect-limit: too many redirects");
    current = safeFetchUrl(new URL(location, current).toString());
  }
  throw new Error("content-redirect-limit: too many redirects");
}

function decodeEntities(value) {
  return value.replace(/&(#x?[0-9a-f]+|amp|lt|gt|quot|apos|nbsp);/gi, (_, entity) => {
    const lower = entity.toLowerCase();
    const named = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };
    if (named[lower]) return named[lower];
    const number = lower.startsWith("#x") ? parseInt(lower.slice(2), 16) : lower.startsWith("#") ? parseInt(lower.slice(1), 10) : NaN;
    return Number.isFinite(number) ? String.fromCodePoint(number) : _;
  });
}

function htmlToText(html) {
  const title = decodeEntities(html.match(/<title[^>]*>([\s\S]*?)<\/title>/i)?.[1] || "").replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
  const body = html
    .replace(/<(script|style|noscript|svg|template)[^>]*>[\s\S]*?<\/\1>/gi, " ")
    .replace(/<br\s*\/?\s*>/gi, "\n")
    .replace(/<\/(p|div|li|article|section|h[1-6]|tr)>/gi, "\n")
    .replace(/<[^>]*>/g, " ");
  const content = decodeEntities(body).replace(/[ \t\r\f]+/g, " ").replace(/\n\s+/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
  return { title, content };
}

async function readBody(response) {
  const length = Number(response.headers.get("content-length") || 0);
  if (length > MAX_BODY_BYTES) throw new Error("content-too-large: response exceeds 5 MiB");
  const buffer = Buffer.from(await response.arrayBuffer());
  if (buffer.length > MAX_BODY_BYTES) throw new Error("content-too-large: response exceeds 5 MiB");
  return buffer.toString("utf8");
}

export async function fetchOneContent(rawUrl, signal) {
  const requested = canonicalUrl(rawUrl) || rawUrl;
  try {
    const { response, url } = await fetchResponse(requested, signal);
    if (!response.ok) throw new Error(`content-http-${response.status}: source returned HTTP ${response.status}`);
    const raw = await readBody(response);
    const type = (response.headers.get("content-type") || "").toLowerCase();
    const parsed = type.includes("html") || /<html[\s>]/i.test(raw) ? htmlToText(raw) : { title: "", content: raw.trim() };
    if (!parsed.content) throw new Error("content-empty: source contained no readable text");
    return { url: canonicalUrl(url) || url, title: compactText(parsed.title, 240), content: parsed.content, error: null, fetchedBy: "http" };
  } catch (error) {
    return { url: requested, title: "", content: "", error: error instanceof Error ? error.message : String(error), fetchedBy: "http" };
  }
}

export async function fetchContents(results, { signal, fallback }: { signal?: AbortSignal; fallback?: (urls: string[], signal?: AbortSignal) => Promise<any[]> } = {}) {
  const queue = results.slice(0, 20);
  const out = [];
  let cursor = 0;
  async function worker() {
    while (cursor < queue.length) {
      const item = queue[cursor++];
      const existing = typeof item.content === "string" && item.content.trim() ? { ...item, error: null } : null;
      if (existing) { out.push(existing); continue; }
      const fetched = await fetchOneContent(item.url, signal);
      if (!fetched.content && fallback) {
        try {
          const alternatives = await fallback([item.url], signal);
          if (alternatives?.[0]?.content) {
            out.push({ ...item, ...alternatives[0], error: null, fetchedBy: alternatives[0].provider || "fallback" });
            continue;
          }
        } catch {
          // Keep the direct-fetch error in the source record.
        }
      }
      out.push({ ...item, ...fetched });
    }
  }
  await Promise.all([worker(), worker(), worker(), worker()]);
  const byUrl = new Map(out.map((item) => [canonicalUrl(item.url) || item.url, item]));
  return results.map((item) => byUrl.get(canonicalUrl(item.url) || item.url) || item);
}
