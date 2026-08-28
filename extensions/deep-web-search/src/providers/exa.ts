import { configuredApiKey } from "../config.js";
import { compactText } from "../normalize.js";
import { splitDomainFilter } from "../normalize.js";

const EXA_API_BASE = "https://api.exa.ai";
const EXA_MCP_URL = "https://mcp.exa.ai/mcp";

function timeoutSignal(signal, ms = 60_000) {
  const timeout = AbortSignal.timeout(ms);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

function dateFromRecency(filter) {
  const days = { day: 1, week: 7, month: 30, year: 365 }[filter];
  return days ? new Date(Date.now() - days * 86_400_000).toISOString() : undefined;
}

function apiArgs(query, options) {
  const { include, exclude } = splitDomainFilter(options.domainFilter);
  return {
    query,
    type: "auto",
    numResults: options.numResults,
    ...(include.length ? { includeDomains: include } : {}),
    ...(exclude.length ? { excludeDomains: exclude } : {}),
    ...(dateFromRecency(options.recencyFilter) ? { startPublishedDate: dateFromRecency(options.recencyFilter) } : {}),
    ...(options.includeContent ? { contents: { text: { maxCharacters: 4_000 }, highlights: { maxCharacters: 1_500 } } } : {}),
  };
}

function mapResults(items, inline = false) {
  if (!Array.isArray(items)) return { results: [], inlineContent: [] };
  const seen = new Set();
  const results = [];
  const inlineContent = [];
  for (const item of items) {
    if (typeof item?.url !== "string" || !item.url.trim() || seen.has(item.url)) continue;
    seen.add(item.url);
    const highlights = Array.isArray(item.highlights) ? item.highlights.filter((x) => typeof x === "string") : [];
    const content = highlights.join(" ") || (typeof item.text === "string" ? item.text : "");
    results.push({ title: typeof item.title === "string" && item.title.trim() ? item.title.trim() : item.url, url: item.url, snippet: compactText(content, 600), provider: "exa", publishedAt: item.publishedDate || undefined });
    if (inline && content) inlineContent.push({ url: item.url, title: item.title || "", content, provider: "exa" });
  }
  return { results, inlineContent };
}

async function apiSearch(query, options, key, signal) {
  const base = process.env.EXA_BASE_URL || EXA_API_BASE;
  const useAnswer = !options.includeContent && !options.recencyFilter && !options.domainFilter?.length && options.numResults === 5;
  const response = await fetch(`${base}/${useAnswer ? "answer" : "search"}`, {
    method: "POST",
    headers: { "x-api-key": key, "Content-Type": "application/json", "x-exa-integration": "ki-deep-web-search" },
    body: JSON.stringify(useAnswer ? { query } : { ...apiArgs(query, options), contents: { highlights: true, ...(options.includeContent ? { text: true } : {}) } }),
    signal: timeoutSignal(signal),
  });
  const raw = await response.text();
  if (!response.ok) throw new Error(`exa-api-http-${response.status}: ${compactText(raw, 260)}`);
  let payload;
  try { payload = JSON.parse(raw); } catch { throw new Error("exa-api-invalid-response: response was not JSON"); }
  const mapped = mapResults(useAnswer ? payload.citations : payload.results, options.includeContent);
  return { answer: useAnswer ? compactText(payload.answer, 5_000) : mapped.results.map((item) => `${item.snippet}\nSource: ${item.title} (${item.url})`).join("\n\n"), ...mapped, provider: "exa", transport: "api" };
}

function mcpQuery(query, options) {
  const parts = [query];
  for (const domain of options.domainFilter || []) parts.push(domain.startsWith("-") ? `-site:${domain.slice(1)}` : `site:${domain}`);
  if (options.recencyFilter) parts.push({ day: "past 24 hours", week: "past week", month: "past month", year: "past year" }[options.recencyFilter]);
  return parts.join(" ");
}

function parseMcpText(text) {
  try {
    const payload = JSON.parse(text);
    if (Array.isArray(payload.results)) return payload.results;
  } catch { /* formatted MCP text is handled below */ }
  return text.split(/(?=^Title: )/m).map((block) => {
    const title = block.match(/^Title:\s*(.+)$/m)?.[1]?.trim() || "";
    const url = block.match(/^URL:\s*(.+)$/m)?.[1]?.trim() || "";
    const textStart = block.indexOf("\nText: ");
    const highlightStart = block.indexOf("\nHighlights:");
    const content = textStart >= 0 ? block.slice(textStart + 7) : highlightStart >= 0 ? block.slice(highlightStart + 12) : "";
    return { title, url, text: content.replace(/\n---\s*$/, "").trim() };
  }).filter((item) => item.url);
}

async function mcpCall(tool, args, signal) {
  const response = await fetch(`${EXA_MCP_URL}?tools=${encodeURIComponent(tool)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream", "x-exa-source": "ki-deep-web-search" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: tool, arguments: args } }),
    signal: timeoutSignal(signal),
  });
  const raw = await response.text();
  if (!response.ok) throw new Error(`exa-mcp-http-${response.status}: ${compactText(raw, 260)}`);
  let rpc;
  for (const line of raw.split(/\r?\n/)) {
    if (!line.startsWith("data:")) continue;
    try {
      const candidate = JSON.parse(line.slice(5).trim());
      if (candidate.result || candidate.error) { rpc = candidate; break; }
    } catch { /* continue */ }
  }
  if (!rpc) {
    try { rpc = JSON.parse(raw); } catch { throw new Error("exa-mcp-invalid-response: response was not JSON-RPC"); }
  }
  if (rpc.error) throw new Error(`exa-mcp-error: ${rpc.error.message || "MCP call failed"}`);
  if (rpc.result?.isError) throw new Error(`exa-mcp-error: ${compactText(rpc.result.content?.find((x) => x.type === "text")?.text, 260)}`);
  const text = rpc.result?.content?.find((x) => x.type === "text" && typeof x.text === "string")?.text;
  if (!text) throw new Error("exa-mcp-empty: MCP returned no content");
  return text;
}

async function mcpSearch(query, options, signal) {
  const filtered = options.includeContent || options.recencyFilter || options.domainFilter?.length;
  const tool = filtered ? "web_search_advanced_exa" : "web_search_exa";
  const args = filtered
    ? { ...apiArgs(query, options), enableHighlights: true, textMaxCharacters: options.includeContent ? 50_000 : 3_000 }
    : { query: mcpQuery(query, options), numResults: options.numResults };
  let raw;
  try {
    raw = await mcpCall(tool, args, signal);
  } catch (error) {
    if (!filtered || String(error).toLowerCase().includes("abort")) throw error;
    raw = await mcpCall("web_search_exa", { query: mcpQuery(query, options), numResults: options.numResults }, signal);
  }
  const items = parseMcpText(raw);
  const mapped = mapResults(items, options.includeContent);
  return { answer: mapped.results.map((item) => `${item.snippet}\nSource: ${item.title} (${item.url})`).join("\n\n"), ...mapped, provider: "exa", transport: "mcp" };
}

export function exaAvailable(config) {
  // Exa's hosted MCP endpoint is the no-key path, so the provider is always
  // selectable; the request diagnostics distinguish MCP from API transport.
  return true;
}

export async function searchExa(query, options, config, signal) {
  const key = configuredApiKey(config, "exaApiKey", "EXA_API_KEY");
  const mode = config.exaMode || "auto";
  if (mode === "api" && !key) throw new Error("exa-auth-missing: exaApiKey is required when exaMode=api");
  if (mode === "mcp" || !key) return mcpSearch(query, options, signal);
  return apiSearch(query, options, key, signal);
}
