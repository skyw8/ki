import { completeWithModel } from "./providers/index.js";
import { compactText } from "./normalize.js";

export function summaryPrompt(result) {
  const sources = result.results || [];
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

export async function generateSummary(result, config, signal) {
  const configured = typeof config.summaryModel === "string" ? config.summaryModel.trim() : "";
  const started = Date.now();
  const output = await completeWithModel(summaryPrompt(result), configured, signal, config.summaryGenerationDeadlineMs);
  return {
    text: output.text,
    meta: { model: output.model, durationMs: Date.now() - started, fallbackUsed: false, phase: "summary" },
  };
}
