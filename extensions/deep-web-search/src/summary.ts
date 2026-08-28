import { completeWithModel } from "./providers/index.js";
import { compactText } from "./normalize.js";

export function summaryPrompt(result, selectedUrls = null) {
  const selected = selectedUrls?.length ? new Set(selectedUrls) : null;
  const sources = (result.results || []).filter((item) => !selected || selected.has(item.url));
  const evidence = sources.map((item, index) => [
    `SOURCE ${index + 1}`,
    `Title: ${item.title}`,
    `URL: ${item.url}`,
    `Snippet: ${compactText(item.snippet, 900)}`,
    item.content ? `Body excerpt: ${compactText(item.content, 4_000)}` : "",
  ].filter(Boolean).join("\n")).join("\n\n");
  return [
    "Write a concise, factual answer to the user's question using only the supplied sources.",
    "Do not invent facts, URLs, quotations, or citations. Mention meaningful disagreement or missing evidence.",
    "Use short headings or bullets when useful and finish with a Sources list containing the URLs used.",
    `User question: ${result.query}`,
    `\nEvidence:\n${evidence || "No readable evidence was returned."}`,
  ].join("\n");
}

export async function generateSummary(result, config, signal, selectedUrls = null) {
  const configured = typeof config.summaryModel === "string" ? config.summaryModel.trim() : "";
  const started = Date.now();
  const output = await completeWithModel(summaryPrompt(result, selectedUrls), configured, signal, config.summaryGenerationDeadlineMs);
  return {
    text: output.text,
    meta: { model: output.model, durationMs: Date.now() - started, fallbackUsed: false, phase: "summary" },
  };
}

export async function rewriteQuery(query, config, signal) {
  const configured = typeof config.queryRewriteModel === "string" ? config.queryRewriteModel.trim() : "";
  if (!configured) throw new Error("query-rewrite-model-missing: queryRewriteModel is empty");
  const output = await completeWithModel([
    "Rewrite the web search query to be more precise while preserving the user's intent.",
    "Return only the rewritten query, with no quotes, explanation, or bullets.",
    `Original query: ${query}`,
  ].join("\n"), configured, signal, config.summaryGenerationDeadlineMs);
  return output.text.replace(/^['"`]+|['"`]+$/g, "").trim().slice(0, 500);
}

export function deterministicSummary(result, selectedUrls = null) {
  const selected = selectedUrls?.length ? new Set(selectedUrls) : null;
  const lines = [`Evidence summary for: ${result.query}`];
  for (const item of result.results || []) {
    if (selected && !selected.has(item.url)) continue;
    lines.push(`- ${item.title}: ${compactText(item.snippet || item.content, 500)} (${item.url})`);
  }
  return lines.join("\n");
}
