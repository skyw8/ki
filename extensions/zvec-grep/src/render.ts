import type { ContextItem, ContextResult } from "./engine.js";

export type PreviewMode = "short" | "full";

export type RenderBudget = {
  preview: PreviewMode;
  maxChars: number;
};

const SHORT_LINES = 12;
const SHORT_CHARS = 1_000;
const FULL_LINES = 80;
const FULL_CHARS = 8_000;
const OUTLINE_LINES = 40;
const DEFAULT_MAX_CHARS = 24_000;

/**
 * Renders the library result as compact, line-numbered text. The shape follows
 * the upstream agent-facing output (status header, then one block per ranked
 * item) so the model pays for reading lines rather than parsing JSON, and the
 * numbers stay usable as Grep/Read anchors.
 */
export function renderContextResult(result: ContextResult, budget?: Partial<RenderBudget>): string {
  const preview = budget?.preview ?? "short";
  const maxChars = budget?.maxChars ?? DEFAULT_MAX_CHARS;
  const lines: string[] = [];
  lines.push(`freshness: ${freshnessOf(result)}`);
  lines.push(`root: ${result.root}`);
  lines.push(`source: ${result.source}`);
  lines.push(`coverage: ${result.coverage}`);
  lines.push(`items: ${result.items.length}`);
  if (result.diagnostics.index?.routes?.length) {
    const routes = result.diagnostics.index.routes.map((route) => `${route.mode}:${truncate(route.query, 80)}`);
    lines.push(`routes: ${routes.join(" | ")}`);
  }
  const notes = diagnosticsNotes(result);
  for (const note of notes) lines.push(`note: ${note}`);
  if (result.items.length === 0) {
    lines.push("");
    lines.push(result.diagnostics.emptyReason === "no_searchable_files"
      ? "No searchable files matched the scope filters for this workspace."
      : "No matches. Try different wording, or use Grep for an exact anchor.");
  }
  result.items.forEach((item, index) => {
    lines.push("");
    lines.push(...itemLines(item, index + 1, preview));
  });
  return clampText(lines.join("\n"), maxChars);
}

export function freshnessOf(result: ContextResult): string {
  if (result.coverage === "ranked_sample") {
    return result.items.some((item) => item.status === "possibly_stale") ? "possibly_stale" : "fresh";
  }
  return result.coverage;
}

function diagnosticsNotes(result: ContextResult): string[] {
  const notes: string[] = [];
  if (result.diagnostics.rg?.truncated) notes.push("ripgrep output was truncated by the result limit");
  if (result.diagnostics.structure?.truncated) {
    notes.push(`structural enrichment stopped early (${result.diagnostics.structure.enrichedItems} enriched items)`);
  }
  return notes;
}

function itemLines(item: ContextItem, ordinal: number, preview: PreviewMode): string[] {
  const location = `${item.file.relativePath}:${item.range.startLine}-${item.range.endLine}`;
  const flags = [
    `#${ordinal}`,
    `rank=${item.rank}`,
    `matchedBy=${item.matchedBy}`,
    typeof item.score === "number" ? `score=${item.score.toFixed(4)}` : "",
    item.status === "possibly_stale" ? "possibly_stale" : "",
  ].filter(Boolean);
  const out = [`${flags.join(" ")} ${location}`];
  const metadata = item.metadata ?? {};
  if (typeof metadata.heading === "string") {
    out.push(`heading: ${metadata.heading}${typeof metadata.level === "number" ? ` (level ${metadata.level})` : ""}`);
  }
  if (typeof metadata.symbolType === "string") out.push(`symbol: ${metadata.symbolType}${typeof metadata.name === "string" ? ` ${metadata.name}` : ""}`);
  if (item.container && item.container.range.startLine !== item.range.startLine) {
    out.push(`container: ${item.container.range.startLine}-${item.container.range.endLine}`);
  }
  if (item.queryGroups?.length) {
    out.push(`groups: ${item.queryGroups.map((group) => `${group.id}(${group.rank})`).join(", ")}`);
  }
  const content = previewContent(item.content, preview);
  if (content) {
    out.push("source:");
    out.push(...numberLines(content, item.range.startLine));
  }
  if (preview === "full" && item.contentRole !== "outline" && item.outline) {
    out.push("outline:");
    out.push(...numberLines(truncateLines(item.outline, OUTLINE_LINES), item.range.startLine));
  }
  return out;
}

function previewContent(content: string, preview: PreviewMode): string {
  const text = preview === "full" ? clampText(content, FULL_CHARS) : clampText(content, SHORT_CHARS);
  return truncateLines(text, preview === "full" ? FULL_LINES : SHORT_LINES);
}

export function numberLines(content: string, startLine: number): string[] {
  const lines = content.replace(/\n$/, "").split("\n");
  return lines.map((line, index) => `${startLine + index}\t${line}`);
}

function truncateLines(text: string, max: number): string {
  const lines = text.split("\n");
  if (lines.length <= max) return text;
  return `${lines.slice(0, max).join("\n")}\n… (${lines.length - max} more lines)`;
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max)}…`;
}

function clampText(text: string, max: number): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max)}\n… (output truncated at ${max} characters)`;
}
