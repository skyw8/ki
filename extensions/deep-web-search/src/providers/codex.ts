import { randomUUID } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, open, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { compactText, splitDomainFilter } from "../normalize.js";

const CODEX_RESPONSES_URL = "https://chatgpt.com/backend-api/codex/responses";
const OPENAI_RESPONSES_URL = "https://api.openai.com/v1/responses";
const AUTH_BASE = "https://auth.openai.com";
const CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann";
const REFRESH_WINDOW_MS = 60_000;
const REQUEST_TIMEOUT_MS = 60_000;

let refreshInFlight;

function credentialsPath() {
  return join(process.env.KI_HOME || "", "credentials.json");
}

function errorText(error) {
  return error instanceof Error ? error.message : String(error);
}

function timeoutSignal(signal, timeoutMs) {
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

function decodeJwtPayload(token) {
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  try {
    const value = parts[1].replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(parts[1].length / 4) * 4, "=");
    const parsed = JSON.parse(Buffer.from(value, "base64").toString("utf8"));
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

function accountFromAccess(access) {
  const payload = decodeJwtPayload(access);
  const auth = payload?.["https://api.openai.com/auth"];
  const account = auth && typeof auth === "object" ? auth.chatgpt_account_id : undefined;
  return typeof account === "string" && account.trim() ? account.trim() : "";
}

function readCredentialDocument() {
  const path = credentialsPath();
  if (!path || !existsSync(path)) throw new Error("codex-auth-missing: KI credentials.json was not found");
  let document;
  try {
    document = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    throw new Error("codex-auth-invalid: KI credentials.json could not be parsed");
  }
  const entry = document?.providers?.["openai-codex"];
  const value = entry?.value;
  if (entry?.type !== "oauth" || !value || typeof value !== "object") {
    throw new Error("codex-auth-missing: openai-codex OAuth credential is not configured");
  }
  const access = typeof value.access === "string" ? value.access.trim() : "";
  const refresh = typeof value.refresh === "string" ? value.refresh.trim() : "";
  const accountId = typeof value.accountId === "string" ? value.accountId.trim() : accountFromAccess(access);
  const expires = Number(value.expires || 0);
  if (!access) throw new Error("codex-auth-invalid: openai-codex access token is empty");
  return { path, document, value, access, refresh, accountId, expires };
}

function isFresh(credential) {
  return credential.expires > Date.now() + REFRESH_WINDOW_MS;
}

async function sleep(ms) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

async function acquireFileLock(path) {
  const lockPath = `${path}.lock`;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const handle = await open(lockPath, "wx", 0o600);
      return { lockPath, handle };
    } catch (error) {
      if (error?.code !== "EEXIST") throw error;
      await sleep(50);
    }
  }
  throw new Error("codex-auth-refresh-busy: credentials.json refresh lock is busy");
}

async function releaseFileLock(lock) {
  try { await lock.handle.close(); } catch { /* lock cleanup is best effort */ }
  try { await rm(lock.lockPath, { force: true }); } catch { /* lock cleanup is best effort */ }
}

async function writeCredentialDocument(path, document) {
  const temp = `${path}.${process.pid}.${randomUUID()}.tmp`;
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  await writeFile(temp, `${JSON.stringify(document, null, 2)}\n`, { mode: 0o600 });
  await rename(temp, path);
}

async function refreshCredential(current) {
  if (!current.refresh) throw new Error("codex-auth-expired: OAuth credential has no refresh token");
  const response = await fetch(`${process.env.KI_CODEX_AUTH_BASE_URL || AUTH_BASE}/oauth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ grant_type: "refresh_token", refresh_token: current.refresh, client_id: CLIENT_ID }),
    signal: timeoutSignal(undefined, 30_000),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`codex-auth-refresh-failed: OAuth token refresh returned HTTP ${response.status}`);
  const access = typeof payload.access_token === "string" ? payload.access_token.trim() : "";
  const refresh = typeof payload.refresh_token === "string" && payload.refresh_token.trim() ? payload.refresh_token.trim() : current.refresh;
  const expiresIn = Number(payload.expires_in || 0);
  if (!access || !expiresIn) throw new Error("codex-auth-refresh-failed: OAuth refresh response was incomplete");
  const nextValue = {
    ...current.value,
    access,
    refresh,
    expires: Date.now() + expiresIn * 1000,
    accountId: accountFromAccess(access) || current.accountId,
  };
  const document = structuredClone(current.document);
  document.providers["openai-codex"].value = nextValue;
  const lock = await acquireFileLock(current.path);
  try {
    // Another Ki process may have refreshed while this process waited for the lock.
    const latest = readCredentialDocument();
    if (isFresh(latest)) return latest;
    await writeCredentialDocument(current.path, document);
  } finally {
    await releaseFileLock(lock);
  }
  return { ...current, document, value: nextValue, access, refresh, accountId: nextValue.accountId, expires: nextValue.expires };
}

async function getCredential() {
  const current = readCredentialDocument();
  if (isFresh(current)) return current;
  if (!refreshInFlight) {
    refreshInFlight = refreshCredential(current).finally(() => { refreshInFlight = undefined; });
  }
  return refreshInFlight;
}

export function codexAvailable() {
  try {
    const credential = readCredentialDocument();
    return Boolean(credential.access && credential.accountId);
  } catch {
    return false;
  }
}

function sourceFilters(options) {
  const { include, exclude } = splitDomainFilter(options.domainFilter);
  if (!include.length && !exclude.length) return undefined;
  return {
    ...(include.length ? { allowed_domains: include } : {}),
    ...(exclude.length ? { blocked_domains: exclude } : {}),
  };
}

function searchInstructions(options) {
  const parts = [
    "Search the web and return a concise answer grounded only in the retrieved sources.",
    "Cite the source URLs in the answer when possible.",
  ];
  const labels = { day: "past 24 hours", week: "past week", month: "past month", year: "past year" };
  if (options.recencyFilter) parts.push(`Prefer sources from the ${labels[options.recencyFilter]}.`);
  if (options.numResults) parts.push(`Prefer around ${options.numResults} distinct sources.`);
  const filters = sourceFilters(options);
  if (filters?.allowed_domains) parts.push(`Only use ${filters.allowed_domains.join(", ")}.`);
  if (filters?.blocked_domains) parts.push(`Do not use ${filters.blocked_domains.join(", ")}.`);
  return parts.join(" ");
}

function responseOutput(payload) {
  if (!payload || typeof payload !== "object") return [];
  if (Array.isArray(payload.output)) return payload.output;
  return [];
}

async function parseResponsesBody(raw) {
  const trimmed = raw.trim();
  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    const parsed = JSON.parse(trimmed);
    return { payload: Array.isArray(parsed) ? { output: parsed } : parsed, streamedText: "", webSearchCallSeen: responseOutput(parsed).some(isSearchCall) };
  }
  const outputItems = [];
  let completed;
  let streamedText = "";
  let webSearchCallSeen = false;
  for (const line of raw.split(/\r?\n/)) {
    if (!line.startsWith("data:")) continue;
    const value = line.slice(5).trim();
    if (!value || value === "[DONE]") continue;
    let event;
    try { event = JSON.parse(value); } catch { continue; }
    if (typeof event.type === "string" && event.type.startsWith("response.web_search_call")) webSearchCallSeen = true;
    if (event.type === "response.output_text.delta" && typeof event.delta === "string") streamedText += event.delta;
    if (event.type === "response.output_item.done" && event.item) {
      outputItems.push(event.item);
      webSearchCallSeen ||= isSearchCall(event.item);
    }
    if ((event.type === "response.done" || event.type === "response.completed") && event.response && typeof event.response === "object") completed = event.response;
  }
  if (completed) {
    const output = responseOutput(completed);
    return { payload: output.length ? completed : { ...completed, output: outputItems }, streamedText, webSearchCallSeen: webSearchCallSeen || output.some(isSearchCall) };
  }
  return { payload: { output: outputItems }, streamedText, webSearchCallSeen: webSearchCallSeen || outputItems.some(isSearchCall) };
}

function isSearchCall(item) {
  return Boolean(item && typeof item === "object" && item.type === "web_search_call");
}

function answerFromOutput(output, streamedText = "") {
  const parts = [];
  for (const item of output) {
    if (!item || typeof item !== "object") continue;
    if (typeof item.output_text === "string") parts.push(item.output_text);
    if (item.type !== "message" || !Array.isArray(item.content)) continue;
    for (const part of item.content) {
      if (typeof part?.text === "string" && part.text.trim()) parts.push(part.text);
    }
  }
  return (parts.join("\n") || streamedText).trim();
}

function snippetAround(value, start, end) {
  if (typeof value !== "string") return "";
  if (!Number.isFinite(start) || !Number.isFinite(end)) return compactText(value, 350);
  const from = Math.max(0, start - 110);
  const to = Math.min(value.length, end + 110);
  return compactText(value.slice(from, to).replace(/\[([^\]]+)\]\([^)]*\)/g, "$1"), 350);
}

function cleanUrl(value) {
  try {
    const url = new URL(value);
    if (url.searchParams.get("utm_source") === "openai") url.searchParams.delete("utm_source");
    url.hash = "";
    return url.toString();
  } catch { return ""; }
}

function sourcesFromOutput(output) {
  const results = [];
  const seen = new Set();
  const add = (value, title, snippet = "") => {
    const url = cleanUrl(value);
    if (!url || seen.has(url)) return;
    seen.add(url);
    results.push({ title: typeof title === "string" && title.trim() ? title.trim() : url, url, snippet: compactText(snippet, 500), provider: "codex" });
  };
  for (const item of output) {
    if (!item || item.type !== "message" || !Array.isArray(item.content)) continue;
    for (const part of item.content) {
      if (!Array.isArray(part?.annotations)) continue;
      for (const annotation of part.annotations) {
        if (annotation?.type !== "url_citation") continue;
        add(annotation.url, annotation.title, snippetAround(part.text, annotation.start_index, annotation.end_index));
      }
    }
  }
  for (const item of output) {
    if (!isSearchCall(item)) continue;
    const groups = [item.action?.sources, item.sources, item.results];
    for (const group of groups) {
      if (!Array.isArray(group)) continue;
      for (const source of group) add(source?.url || source?.source_website_url, source?.title || source?.caption, source?.text || source?.snippet);
    }
  }
  return results;
}

async function responsesRequest({ model, prompt, search, signal, provider = "openai-codex" }: { model: string; prompt: string; search?: any; signal: AbortSignal; provider?: string }) {
  const credential = provider === "openai" ? await openAiCredential() : await getCredential();
  const codex = provider !== "openai";
  const endpoint = codex
    ? (process.env.KI_DEEP_WEB_SEARCH_CODEX_URL || CODEX_RESPONSES_URL)
    : (process.env.KI_DEEP_WEB_SEARCH_OPENAI_URL || OPENAI_RESPONSES_URL);
  const headers: Record<string, string> = {
    Authorization: `Bearer ${credential.access}`,
    "Content-Type": "application/json",
    "OpenAI-Beta": "responses=experimental",
  };
  if (codex && credential.accountId) {
    headers["chatgpt-account-id"] = credential.accountId;
    headers.originator = "pi";
  }
  const body = {
    model,
    instructions: search ? searchInstructions(search) : "Answer using only the supplied evidence. Do not invent citations or facts.",
    input: [{ role: "user", content: [{ type: "input_text", text: prompt }] }],
    store: false,
    stream: true,
    ...(search ? {
      tools: [{ type: "web_search", ...(sourceFilters(search) ? { filters: sourceFilters(search) } : {}) }],
      include: ["web_search_call.action.sources"],
      tool_choice: "required",
      parallel_tool_calls: true,
    } : {}),
  };
  let response;
  try {
    response = await fetch(endpoint, { method: "POST", headers, body: JSON.stringify(body), signal: timeoutSignal(signal, search ? REQUEST_TIMEOUT_MS : 30_000) });
  } catch (error) {
    throw new Error(`codex-network-error: ${errorText(error)}`);
  }
  const raw = await response.text();
  if (!response.ok) throw new Error(`codex-http-${response.status}: ${compactText(raw, 260)}`);
  let parsed;
  try { parsed = await parseResponsesBody(raw); } catch (error) { throw new Error(`codex-invalid-response: ${errorText(error)}`); }
  return { ...parsed, credential };
}

async function openAiCredential() {
  const access = typeof process.env.OPENAI_API_KEY === "string" ? process.env.OPENAI_API_KEY.trim() : "";
  if (!access) throw new Error("openai-auth-missing: OPENAI_API_KEY is not configured");
  return { access, accountId: "" };
}

export async function searchCodex(query, options, signal) {
  const model = typeof options.codexModel === "string" && options.codexModel.trim() ? options.codexModel.trim() : "gpt-5.5";
  const response = await responsesRequest({ model, prompt: query, search: options, signal });
  const output = responseOutput(response.payload);
  const results = sourcesFromOutput(output).slice(0, options.numResults);
  const answer = answerFromOutput(output, response.streamedText);
  if (!answer && !results.length) throw new Error("codex-empty-search: Codex returned no answer or sources");
  return { answer, results, provider: "codex", model };
}

function modelSpec(value) {
  const spec = typeof value === "string" ? value.trim() : "";
  if (!spec) return null;
  const slash = spec.indexOf("/");
  if (slash < 0) return { provider: "openai-codex", model: spec };
  return { provider: spec.slice(0, slash), model: spec.slice(slash + 1) };
}

export async function completeWithModel(prompt, configuredModel, signal, deadlineMs = 30_000) {
  const target = modelSpec(configuredModel);
  if (!target?.model) throw new Error("summary-model-missing: summaryModel is empty");
  if (target.provider !== "openai-codex" && target.provider !== "openai") {
    throw new Error(`summary-model-unsupported: ${target.provider} is not supported by the sidecar completion adapter`);
  }
  const deadline = timeoutSignal(signal, deadlineMs);
  const response = await responsesRequest({ model: target.model, prompt, signal: deadline, provider: target.provider });
  const answer = answerFromOutput(responseOutput(response.payload), response.streamedText);
  if (!answer) throw new Error("summary-empty: model returned no text");
  return { text: answer, model: `${target.provider}/${target.model}` };
}
