import { randomUUID } from "node:crypto";
import { configPath, invalidateConfig, loadConfig, normalizeConfig } from "./config.js";
import { SearchCache } from "./cache.js";
import { aggregateSearch, combineAggregates, sourcePackText } from "./aggregate.js";
import { fetchContents, fetchOneContent } from "./content.js";
import { buildEvidence, sourceCheckClaims } from "./evidence.js";
import { generateSummary } from "./summary.js";
import { normalizeOptions, normalizeQueries, validateProxy } from "./normalize.js";
import { fetchTinyfish, tinyfishAvailable } from "./providers/index.js";
import { StdioRpc, safeError } from "./rpc.js";

const PROVIDER_VALUES = ["auto", "all", "codex", "exa", "tinyfish", "duckduckgo"];
const PROVIDER_ARRAY = ["codex", "exa", "tinyfish", "duckduckgo"];

export const TOOL_SPECS = [
  {
    name: "deep_web_search",
    description: "Search the web through Codex OAuth, Exa, TinyFish, and DuckDuckGo in parallel; aggregate and clean distinct sources.",
    snippet: "Search multiple independent web indexes and return a deduplicated evidence pack.",
    timeoutMs: 120_000,
    parameters: {
      type: "object",
      additionalProperties: false,
      properties: {
        query: { type: "string", description: "One search query." },
        queries: { type: "array", items: { type: "string" }, maxItems: 8, description: "Multiple queries; takes precedence over query." },
        numResults: { type: "integer", minimum: 1, maximum: 20, default: 5 },
        includeContent: { type: "boolean", default: false, description: "Fetch source bodies in the background." },
        recencyFilter: { type: "string", enum: ["day", "week", "month", "year"] },
        domainFilter: { type: "array", items: { type: "string" }, description: "Include domains or exclude them with a leading '-'." },
        provider: { oneOf: [{ type: "string", enum: PROVIDER_VALUES }, { type: "array", items: { type: "string", enum: PROVIDER_ARRAY }, minItems: 1, maxItems: 4 }] },
        proxy: { type: "string", description: "Reserved HTTP(S) proxy override; direct fetch is used when empty." },
      },
    },
  },
  {
    name: "fetch_content",
    description: "Fetch readable text from one or more safe public HTTP(S) source URLs.",
    parameters: { type: "object", additionalProperties: false, properties: { url: { type: "string" }, urls: { type: "array", items: { type: "string" } }, purpose: { type: "string" } } },
  },
  {
    name: "get_search_content",
    description: "Read source bodies saved by deep_web_search by responseId, with optional offset, limit, or findText.",
    parameters: { type: "object", additionalProperties: false, properties: { responseId: { type: "string" }, urls: { type: "array", items: { type: "string" } }, offset: { type: "integer" }, limit: { type: "integer" }, findText: { type: "string" } } },
  },
  {
    name: "source_check",
    description: "Search for evidence and deterministically assess claims against returned source passages.",
    parameters: { type: "object", additionalProperties: false, properties: { query: { type: "string" }, claims: { type: "array", items: { type: "string" } }, claim: { type: "string" }, numResults: { type: "integer", minimum: 1, maximum: 20 }, provider: { oneOf: [{ type: "string", enum: PROVIDER_VALUES }, { type: "array", items: { type: "string", enum: PROVIDER_ARRAY } }] } } },
  },
];

const rpc = new StdioRpc();
const cache = new SearchCache(process.env.KI_EXTENSION_ROOT || process.cwd());
const activeCalls = new Map();

function recordProgress(id, toolCallId, partial) {
  if (id === undefined || id === null) return;
  rpc.notify("tool.progress", { id: String(id), toolCallId: toolCallId || "", partial });
}

function asObject(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function selectedProvider(args, config) {
  if (Array.isArray(args?.provider)) return args.provider;
  if (typeof args?.provider === "string" && PROVIDER_VALUES.includes(args.provider)) return args.provider;
  return config.provider;
}

async function withTimeout(promise, ms) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("host rpc timeout")), ms); })]);
  } finally { clearTimeout(timer); }
}

async function persistSearchEntry(sessionId, result, details) {
  if (!sessionId) return;
  const data = {
    responseId: result.responseId,
    query: result.query,
    results: (result.results || []).map((item) => ({
      title: item.title,
      url: item.url,
      snippet: item.snippet,
      providers: item.providers,
      ranks: item.ranks,
      score: item.score,
      content: typeof item.content === "string" ? item.content.slice(0, 20_000) : "",
      contentError: item.error || item.contentError || "",
    })),
    diagnostics: result.diagnostics,
    details,
  };
  try {
    await withTimeout(rpc.call("session.appendEntry", { sessionId, customType: "deep-web-search-results", data }), 2_000);
  } catch {
    // Tool execution remains useful when a direct sidecar test has no Host.
  }
  const content = data.results.filter((item) => item.content);
  if (content.length) {
    try {
      await withTimeout(rpc.call("session.appendEntry", {
        sessionId,
        customType: "deep-web-search-content-ready",
        data: { responseId: result.responseId, contents: content.map((item) => ({ url: item.url, title: item.title, content: item.content })) },
      }), 2_000);
    } catch {
      // Content remains available through get_search_content even if the
      // optional session entry cannot be appended.
    }
  }
}

async function runSearch(query, options, config, signal, id, toolCallId) {
  const key = cache.key(query, options);
  const cached = cache.get(key);
  if (cached) {
    recordProgress(id, toolCallId, { phase: "cache", status: "hit", query });
    return { ...cached, cacheHit: true };
  }
  const result = await aggregateSearch(query, options, config, signal, (partial) => recordProgress(id, toolCallId, { query, ...partial }));
  cache.put(key, result);
  cache.response(result.responseId, result);
  return { ...result, cacheHit: false };
}

function mergeOptions(args, config) {
  const options = normalizeOptions({ ...args, provider: selectedProvider(args, config) }, config);
  options.proxy = validateProxy(options.proxy);
  return options;
}

function detailsFor(requestedWorkflow, effectiveWorkflow, result, extra: any = {}) {
  return {
    responseId: result.responseId,
    requestedWorkflow,
    effectiveWorkflow,
    fallbackTo: extra.fallbackTo ?? null,
    fallbackReason: extra.fallbackReason ?? null,
    query: result.query,
    providerRuns: result.runs?.map((run) => ({ provider: run.provider, transport: run.transport, count: run.results?.length || 0 })) || [],
    diagnostics: result.diagnostics || [],
    cacheHit: result.cacheHit === true,
    ...extra,
  };
}

function sourceResult(textValue, details, isError = false) {
  return { content: [{ type: "text", text: textValue }], details, ...(isError ? { isError: true } : {}) };
}

async function fetchTool(args, config, signal) {
  const urls = [...new Set([...(typeof args.url === "string" ? [args.url] : []), ...(Array.isArray(args.urls) ? args.urls.filter((item) => typeof item === "string") : [])])].slice(0, 20);
  if (!urls.length) return sourceResult("fetch_content requires url or urls", {}, true);
  const fallback = config.providerToggles?.tinyfish !== false && tinyfishAvailable(config)
    ? (items, fetchSignal) => fetchTinyfish(items, config, fetchSignal)
    : undefined;
  const results = await fetchContents(urls.map((url) => ({ url, title: url, snippet: "" })), { signal, fallback });
  const textValue = results.map((item) => `${item.title || item.url}\n${item.url}\n${item.content ? item.content.slice(0, 6_000) : `ERROR: ${item.error || "empty"}`}`).join("\n\n");
  return sourceResult(textValue, { urls, fetched: results.map((item) => ({ url: item.url, bytes: item.content?.length || 0, error: item.error || null, fetchedBy: item.fetchedBy || null })) });
}

function contentFromResult(result, args) {
  const requestedUrls = Array.isArray(args.urls) ? new Set(args.urls) : null;
  let items = (result.results || []).filter((item) => !requestedUrls || requestedUrls.has(item.url));
  const chunks = [];
  for (const item of items) {
    let content = item.content || "";
    if (args.findText && content) {
      const at = content.toLowerCase().indexOf(String(args.findText).toLowerCase());
      if (at >= 0) content = content.slice(Math.max(0, at - 500), at + 2_500);
    }
    const offset = Number.isFinite(Number(args.offset)) ? Math.max(0, Number(args.offset)) : 0;
    const limit = Number.isFinite(Number(args.limit)) ? Math.max(1, Math.min(50_000, Number(args.limit))) : 8_000;
    content = content.slice(offset, offset + limit);
    chunks.push(`${item.title}\n${item.url}\n${content || `[no content${item.contentError ? `: ${item.contentError}` : ""}]`}`);
  }
  return chunks.join("\n\n");
}

async function getContentTool(args) {
  const response = typeof args.responseId === "string" ? cache.responseValue(args.responseId) : null;
  if (!response) return sourceResult("get_search_content: responseId was not found or has expired", {}, true);
  return sourceResult(contentFromResult(response, args), { responseId: args.responseId, urls: args.urls || null, offset: args.offset || 0, limit: args.limit || 8_000 });
}

async function sourceCheckTool(args, config, signal, id, toolCallId) {
  const query = typeof args.query === "string" ? args.query.trim() : "";
  const claims = [...(Array.isArray(args.claims) ? args.claims : []), ...(typeof args.claim === "string" ? [args.claim] : [])].filter((item) => typeof item === "string" && item.trim()).map((item) => item.trim()).slice(0, 20);
  if (!query || !claims.length) return sourceResult("source_check requires query and claim or claims", {}, true);
  const options = mergeOptions({ ...args, includeContent: true }, config);
  const result = await runSearch(query, options, config, signal, id, toolCallId);
  const evidence = buildEvidence(result);
  const assessments = sourceCheckClaims(claims, evidence);
  const details = { responseId: result.responseId, evidence, assessments, diagnostics: result.diagnostics };
  return sourceResult(JSON.stringify({ query, assessments }, null, 2), details);
}

async function deepSearchTool(args, config, signal, id, toolCallId, sessionId) {
  const queries = normalizeQueries(args);
  if (!queries.length) return sourceResult("deep_web_search requires query or queries", {}, true);
  const options = mergeOptions(args, config);
  // Workflow is intentionally read only from extension config. Exposing it in
  // the tool schema would let the model override the user's global setting.
  const requestedWorkflow = config.workflow;
  // Queries are independent research angles. Start them together, but retain
  // per-query failures so one slow or failed angle cannot discard good results.
  const settled = await Promise.allSettled(queries.map(async (query) => {
    recordProgress(id, toolCallId, { phase: "query", status: "running", query });
    try {
      const aggregate = await runSearch(query, options, config, signal, id, toolCallId);
      recordProgress(id, toolCallId, { phase: "query", status: "done", query, count: aggregate.results?.length || 0 });
      return aggregate;
    } catch (error) {
      recordProgress(id, toolCallId, { phase: "query", status: "failed", query, error: safeError(error) });
      throw error;
    }
  }));
  const aggregates = [];
  const queryFailures = [];
  settled.forEach((item, index) => {
    if (item.status === "fulfilled") {
      aggregates.push(item.value);
      return;
    }
    queryFailures.push({ query: queries[index], error: safeError(item.reason) });
  });
  if (!aggregates.length) {
    throw new Error(`query-failed: ${queryFailures.map((item) => `${item.query}: ${item.error}`).join("; ")}`);
  }
  let result = queries.length === 1 ? aggregates[0] : combineAggregates(queries.join("; "), aggregates, options);
  if (queryFailures.length) {
    result = {
      ...result,
      diagnostics: [
        ...(result.diagnostics || []),
        ...queryFailures.map((item) => ({ query: item.query, ok: false, category: "query", error: item.error })),
      ],
    };
  }
  cache.response(result.responseId, result);
  if (requestedWorkflow === "none") {
    const details = detailsFor(requestedWorkflow, "none", result, { sourceCount: result.results.length, contentAvailable: result.results.some((item) => item.content), queryFailures });
    await persistSearchEntry(sessionId, result, details);
    return sourceResult(sourcePackText(result), details);
  }
  if (requestedWorkflow === "auto-summary") {
    try {
      recordProgress(id, toolCallId, { phase: "summary", status: "running" });
      const summary = await generateSummary(result, config, signal);
      const details = detailsFor(requestedWorkflow, "auto-summary", result, { summary: summary.meta, sourceCount: result.results.length, queryFailures });
      await persistSearchEntry(sessionId, result, details);
      return sourceResult(summary.text, details);
    } catch (error) {
      const details = detailsFor(requestedWorkflow, "none", result, { fallbackTo: "none", fallbackReason: safeError(error), summary: { fallbackUsed: true, fallbackReason: safeError(error), phase: "summary" }, sourceCount: result.results.length, queryFailures });
      await persistSearchEntry(sessionId, result, details);
      return sourceResult(sourcePackText(result), details);
    }
  }
  throw new Error(`workflow-unsupported: ${requestedWorkflow}`);
}

async function executeTool(params, id) {
  const args = asObject(params.args);
  const config = normalizeConfig(loadConfig());
  if (params.name !== "deep_web_search" && params.name !== "fetch_content" && params.name !== "get_search_content" && params.name !== "source_check") return sourceResult(`unknown tool ${params.name || ""}`, {}, true);
  const controller = new AbortController();
  activeCalls.set(String(id), controller);
  try {
    if (params.name === "get_search_content") return await getContentTool(args);
    if (params.name === "fetch_content") return await fetchTool(args, config, controller.signal);
    if (params.name === "source_check") return await sourceCheckTool(args, config, controller.signal, id, params.toolCallId);
    return await deepSearchTool(args, config, controller.signal, id, params.toolCallId, typeof params.sessionId === "string" ? params.sessionId : "");
  } catch (error) {
    const message = safeError(error);
    return sourceResult(message, { error: message }, true);
  } finally {
    activeCalls.delete(String(id));
  }
}

rpc.onRequest(async (method, params, id) => {
  switch (method) {
    case "initialize": return { tools: TOOL_SPECS };
    case "config.updated": invalidateConfig(); return {};
    case "cancel": {
      const controller = activeCalls.get(String(params.id || ""));
      controller?.abort(new Error("tool call cancelled"));
      return {};
    }
    case "tool.execute": return executeTool(params, id);
    case "shutdown":
      for (const controller of activeCalls.values()) controller.abort(new Error("sidecar shutdown"));
      return {};
    case "session.open":
    case "session.close":
      return {};
    default:
      return {};
  }
});

// Reading the path here makes startup failures visible in a sidecar stderr
// line while still letting the Host finish initialize for no-auth DDG/Exa.
if (process.env.KI_DEEP_WEB_SEARCH_VALIDATE_CONFIG === "1") {
  try { loadConfig(); } catch (error) { process.stderr.write(`${configPath()}: ${safeError(error)}\n`); }
}

rpc.start();
