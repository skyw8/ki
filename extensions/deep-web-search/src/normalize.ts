const TRACKING_PARAMS = new Set([
  "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "gclid", "fbclid", "ref", "source",
]);

export function text(value, fallback = "") {
  return typeof value === "string" ? value.trim() : fallback;
}

export function normalizeQueries(args) {
  const many = Array.isArray(args?.queries)
    ? args.queries.filter((item) => typeof item === "string").map((item) => item.trim()).filter(Boolean)
    : [];
  if (many.length) return [...new Set(many)].slice(0, 8);
  const one = text(args?.query);
  return one ? [one] : [];
}

export function clampResults(value, fallback = 5) {
  if (!Number.isFinite(Number(value))) return fallback;
  return Math.max(1, Math.min(20, Math.floor(Number(value))));
}

export function normalizeOptions(args, config) {
  const provider = Array.isArray(args?.provider)
    ? [...new Set(args.provider.filter((item) => typeof item === "string").map((item) => item.trim()).filter(Boolean))]
    : text(args?.provider, text(config?.provider, "all"));
  const domains = Array.isArray(args?.domainFilter)
    ? args.domainFilter.filter((item) => typeof item === "string").map((item) => item.trim()).filter(Boolean).slice(0, 100)
    : [];
  return {
    numResults: clampResults(args?.numResults, clampResults(config?.maxResults, 5)),
    includeContent: args?.includeContent === true || config.fetchContent === true,
    recencyFilter: ["day", "week", "month", "year"].includes(args?.recencyFilter) ? args.recencyFilter : undefined,
    domainFilter: domains,
    provider,
    proxy: text(args?.proxy),
  };
}

export function normalizeDomain(raw) {
  let value = text(raw).toLowerCase();
  if (value.startsWith("-")) value = value.slice(1).trim();
  if (!value) return null;
  try {
    value = new URL(value.includes("://") ? value : `https://${value}`).hostname;
  } catch {
    value = value.split("/")[0].split(":")[0];
  }
  value = value.replace(/^\.+|\.+$/g, "");
  return /^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$/i.test(value) ? value : null;
}

export function splitDomainFilter(values = []) {
  const include = [];
  const exclude = [];
  for (const raw of values) {
    const domain = normalizeDomain(raw);
    if (!domain) continue;
    const bucket = text(raw).startsWith("-") ? exclude : include;
    if (!bucket.includes(domain)) bucket.push(domain);
  }
  return { include, exclude };
}

export function canonicalUrl(raw) {
  try {
    const url = new URL(raw);
    if (url.protocol !== "http:" && url.protocol !== "https:") return null;
    url.hash = "";
    for (const key of [...url.searchParams.keys()]) {
      if (TRACKING_PARAMS.has(key.toLowerCase()) || key.toLowerCase().startsWith("utm_")) url.searchParams.delete(key);
    }
    url.hostname = url.hostname.toLowerCase();
    if ((url.protocol === "https:" && url.port === "443") || (url.protocol === "http:" && url.port === "80")) url.port = "";
    if (url.pathname.length > 1) url.pathname = url.pathname.replace(/\/+$/, "");
    return url.toString();
  } catch {
    return null;
  }
}

export function hostOf(url) {
  try { return new URL(url).hostname.toLowerCase(); } catch { return ""; }
}

export function compactText(value, max = 1000) {
  const clean = text(value).replace(/\s+/g, " ");
  return clean.length > max ? `${clean.slice(0, Math.max(0, max - 3))}...` : clean;
}

export function tokens(value) {
  return new Set(compactText(value, 5000).toLowerCase().match(/[\p{L}\p{N}]{2,}/gu) ?? []);
}

export function tokenSimilarity(a, b) {
  const left = tokens(a);
  const right = tokens(b);
  if (!left.size || !right.size) return 0;
  let common = 0;
  for (const item of left) if (right.has(item)) common++;
  return common / (left.size + right.size - common);
}

export function validateProxy(value) {
  if (!value) return null;
  let url;
  try { url = new URL(value); } catch { throw new Error("proxy must be a valid HTTP(S) URL"); }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("proxy must use http:// or https://");
  if (!url.hostname) throw new Error("proxy must include a host");
  return url.toString();
}
