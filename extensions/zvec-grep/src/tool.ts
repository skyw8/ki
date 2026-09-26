import { statSync } from "node:fs";
import { cliAvailable, runCli, zgBinary } from "./cli.js";
import type { ZvecConfig } from "./config.js";
import {
  type ContextResult,
  EnginePool,
  engineError,
  SearchCancelledError,
  SearchTimeoutError,
} from "./engine.js";
import { type PreviewMode, renderContextResult } from "./render.js";

export type ToolContent = { type: "text"; text: string };

export type ToolResult = {
  content: ToolContent[];
  details?: Record<string, unknown>;
  isError?: boolean;
};

export type SearchPlan = {
  root: string;
  queries: string[];
  fts: string[];
  vector: string[];
  fuse: boolean;
  limit: number;
  preview: PreviewMode;
  freshness: "wait_for_fresh" | "eventual";
  globs?: string[];
  insensitiveGlobs?: string[];
  fileTypes?: string[];
  excludedFileTypes?: string[];
  ignoreFiles?: string[];
  hidden?: boolean;
  noIgnore?: boolean;
  preferSymbol?: boolean;
  symbolTypes?: string[];
  maxDepth?: number;
  maxFileSizeBytes?: number;
  modifiedAfter?: number;
  modifiedBefore?: number;
};

export type ToolContext = {
  cwd: string;
  config: ZvecConfig;
  pool: EnginePool;
  /** onPlan reports the resolved plan so callers can name the searched root. */
  onPlan?: (plan: SearchPlan) => void;
};

const SYMBOL_TYPES = ["module", "class", "interface", "function", "value", "alias"];
const MAX_QUERIES = 8;

export const SEARCH_TOOL_NAME = "zvec_grep_search";

export const SEARCH_TOOL_SPEC = {
  name: SEARCH_TOOL_NAME,
  description:
    "Search an existing zvec-grep index of the workspace: hybrid lexical (BM25) + vector retrieval for semantic, fuzzy, relationship, and cross-file questions whose wording or location is unknown. " +
    "Use the built-in Grep/Glob instead for exact words, names, paths, keys, and regexes. " +
    "Results are a ranked sample with file:line anchors, so follow up with Read or Grep. " +
    "The index is workspace-scoped and user-managed: never build, rebuild, or delete it yourself; report a missing index and let the user run /zg-index.",
  snippet: "Ranked hybrid (lexical + vector) search over a local zvec-grep index.",
  // Host-side hard limit; the sidecar's own budget (config.searchTimeoutMs)
  // stays below it so a slow search degrades to an explanatory error first.
  timeoutMs: 120_000,
  parameters: {
    type: "object",
    additionalProperties: false,
    properties: {
      query: { type: "string", description: "One hybrid natural-language query." },
      queries: { type: "array", items: { type: "string" }, maxItems: MAX_QUERIES, description: "Additional hybrid query groups." },
      fts: { type: "array", items: { type: "string" }, maxItems: MAX_QUERIES, description: "Ranked lexical query groups inside the index; not exhaustive occurrence lookup." },
      vector: { type: "array", items: { type: "string" }, maxItems: MAX_QUERIES, description: "Semantic-only query groups." },
      fuse: { type: "boolean", description: "Fuse every query group into one ranked plan." },
      limit: { type: "integer", minimum: 1, maximum: 50, description: "Maximum items per group." },
      preview: { type: "string", enum: ["short", "full"], description: "short (default) keeps a bounded snippet per hit; full adds the complete content and outline." },
      freshness: {
        type: "string",
        enum: ["wait_for_fresh", "eventual"],
        description: "wait_for_fresh (default) refreshes changed files before searching; eventual answers immediately and marks stale items.",
      },
      root: { type: "string", description: "Absolute workspace root; defaults to the session working directory." },
      globs: { type: "array", items: { type: "string" }, description: "Ordered path rules, e.g. src/** or !**/dist/**." },
      insensitiveGlobs: { type: "array", items: { type: "string" }, description: "Case-insensitive path rules." },
      fileTypes: { type: "array", items: { type: "string" }, description: "ripgrep file types, e.g. ts or md." },
      excludedFileTypes: { type: "array", items: { type: "string" }, description: "ripgrep file types to exclude." },
      ignoreFiles: { type: "array", items: { type: "string" }, description: "Extra ignore files relative to the root." },
      hidden: { type: "boolean", description: "Include hidden files." },
      noIgnore: { type: "boolean", description: "Do not respect .gitignore." },
      preferSymbol: { type: "boolean", description: "Prefer an exact indexed symbol." },
      symbolTypes: { type: "array", items: { type: "string", enum: SYMBOL_TYPES }, description: "Restrict results to these symbol types." },
      maxDepth: { type: "integer", minimum: 0, description: "Maximum recursive directory depth." },
      maxFileSizeBytes: { type: "integer", minimum: 1, description: "Maximum indexed file size." },
      // oneOf instead of a type array: some provider tool schemas reject arrays.
      modifiedAfter: {
        oneOf: [{ type: "string" }, { type: "integer" }],
        description: "Only files modified after this ISO date or epoch milliseconds.",
      },
      modifiedBefore: {
        oneOf: [{ type: "string" }, { type: "integer" }],
        description: "Only files modified before this ISO date or epoch milliseconds.",
      },
    },
  },
} as const;

export type Normalized = { plan: SearchPlan } | { error: string };

/** normalizeSearchArgs validates the model input and applies config defaults. */
export function normalizeSearchArgs(args: Record<string, unknown>, ctx: ToolContext): Normalized {
  const root = rootOf(args, ctx.cwd);
  if ("error" in root) return { error: root.error };
  const queries = stringList(args.queries);
  const single = typeof args.query === "string" ? args.query.trim() : "";
  const fts = stringList(args.fts);
  const vector = stringList(args.vector);
  if (single) queries.unshift(single);
  if (!queries.length && !fts.length && !vector.length) {
    return { error: "zvec_grep_search requires query, queries, fts, or vector" };
  }
  if (queries.length + fts.length + vector.length > MAX_QUERIES * 3) {
    return { error: `too many query groups (max ${MAX_QUERIES} per kind)` };
  }
  const modifiedAfter = modifiedTime(args.modifiedAfter);
  if (modifiedAfter === null) return { error: "modifiedAfter must be an ISO date or epoch milliseconds" };
  const modifiedBefore = modifiedTime(args.modifiedBefore);
  if (modifiedBefore === null) return { error: "modifiedBefore must be an ISO date or epoch milliseconds" };
  const preview = args.preview === "full" ? "full" : "short";
  const symbolTypes = stringList(args.symbolTypes).filter((item) => SYMBOL_TYPES.includes(item));
  return {
    plan: {
      root: root.path,
      queries,
      fts,
      vector,
      fuse: args.fuse === true,
      limit: clampInteger(args.limit, 1, 50, ctx.config.maxResults),
      preview,
      freshness: args.freshness === "eventual" ? "eventual" : "wait_for_fresh",
      globs: optionalList(args.globs),
      insensitiveGlobs: optionalList(args.insensitiveGlobs),
      fileTypes: optionalList(args.fileTypes),
      excludedFileTypes: optionalList(args.excludedFileTypes),
      ignoreFiles: optionalList(args.ignoreFiles),
      hidden: args.hidden === true ? true : undefined,
      noIgnore: args.noIgnore === true ? true : undefined,
      preferSymbol: args.preferSymbol === true ? true : undefined,
      symbolTypes: symbolTypes.length ? symbolTypes : undefined,
      maxDepth: optionalInteger(args.maxDepth, 0),
      maxFileSizeBytes: optionalInteger(args.maxFileSizeBytes, 1),
      modifiedAfter: modifiedAfter ?? undefined,
      modifiedBefore: modifiedBefore ?? undefined,
    },
  };
}

/** contextOptions maps the plan onto the library's context() options. */
export function contextOptions(plan: SearchPlan, config: ZvecConfig): Record<string, unknown> {
  return {
    root: plan.root,
    queries: plan.queries.length ? plan.queries : undefined,
    routes: [
      ...plan.fts.map((query) => ({ mode: "fts", query })),
      ...plan.vector.map((query) => ({ mode: "vector", query })),
    ],
    fuse: plan.fuse,
    limit: plan.limit,
    // ignoredGlobs is user policy, so it always applies and cannot be widened
    // by a model-supplied path rule.
    excludePaths: config.ignoredGlobs.length ? config.ignoredGlobs : undefined,
    globs: plan.globs,
    insensitiveGlobs: plan.insensitiveGlobs,
    fileTypes: plan.fileTypes,
    excludedFileTypes: plan.excludedFileTypes,
    ignoreFiles: plan.ignoreFiles,
    hidden: plan.hidden,
    noIgnore: plan.noIgnore,
    preferSymbol: plan.preferSymbol,
    symbolTypes: plan.symbolTypes,
    maxDepth: plan.maxDepth,
    maxFileSizeBytes: plan.maxFileSizeBytes,
    modifiedAfter: plan.modifiedAfter,
    modifiedBefore: plan.modifiedBefore,
    // "wait_for_fresh" is the library's refresh-then-search path; "eventual"
    // skips the refresh and reports possibly_stale items instead.
    autoUpdate: plan.freshness === "wait_for_fresh",
  };
}

/** executeSearch runs one search through the library and renders the result. */
export async function executeSearch(args: Record<string, unknown>, ctx: ToolContext): Promise<ToolResult> {
  const normalized = normalizeSearchArgs(args, ctx);
  if ("error" in normalized) return errorResult(normalized.error);
  const { plan } = normalized;
  ctx.onPlan?.(plan);
  if (ctx.config.mode === "external-daemon") return runCliSearch(plan, ctx);
  const started = Date.now();
  try {
    const result = await ctx.pool.run(ctx.config, ctx.config.searchTimeoutMs, (engine) =>
      engine.context(contextOptions(plan, ctx.config)),
    );
    return successResult(result, plan, Date.now() - started);
  } catch (error) {
    return failureResult(error, plan, Date.now() - started);
  }
}

export function successResult(result: ContextResult, plan: SearchPlan, durationMs: number): ToolResult {
  const text = renderContextResult(result, { preview: plan.preview });
  return {
    content: [{ type: "text", text }],
    details: {
      root: result.root,
      source: result.source,
      coverage: result.coverage,
      items: result.items.length,
      workspaceIndex: result.workspaceIndex?.id ?? null,
      stale: result.items.filter((item) => item.status === "possibly_stale").length,
      emptyReason: result.diagnostics.emptyReason ?? null,
      routes: result.diagnostics.index?.routes?.map((route) => `${route.mode}:${route.query}`) ?? [],
      durationMs,
    },
  };
}

/** failureResult turns a library failure into model-actionable text. */
export function failureResult(error: unknown, plan: SearchPlan, durationMs: number): ToolResult {
  const details = { root: plan.root, durationMs };
  if (error instanceof SearchTimeoutError) {
    return {
      content: [{
        type: "text",
        text: `zvec_grep_search timed out after ${durationMs}ms for ${plan.root}. ` +
          "The search may still be refreshing the index. Retry once, narrow the query or globs, or use Grep for an exact anchor.",
      }],
      details: { ...details, timeout: true },
      isError: true,
    };
  }
  if (error instanceof SearchCancelledError) return cancelledResult(plan);
  const info = engineError(error);
  if (info.code === "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_NOT_FOUND") {
    return {
      content: [{
        type: "text",
        text: `No zvec-grep index for ${plan.root}.\n` +
          "This tool never builds an index. Tell the user the workspace is not indexed and that /zg-index (or `zg index`) creates one; " +
          "until then use Grep/Glob for exact lookups.",
      }],
      details: { ...details, code: info.code },
      isError: true,
    };
  }
  if (info.code === "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_DISABLED") {
    return {
      content: [{
        type: "text",
        text: `The zvec-grep index of ${plan.root} is disabled. Do not rebuild it; use Grep/Glob, or let the user re-enable it with /zg-index.`,
      }],
      details: { ...details, code: info.code, agentAction: "do_not_build_index" },
      isError: true,
    };
  }
  return {
    content: [{ type: "text", text: `zvec_grep_search failed: ${info.message}${info.details ? `\n${info.details}` : ""}` }],
    details: { ...details, code: info.code || null },
    isError: true,
  };
}

export function cancelledResult(plan: Pick<SearchPlan, "root">): ToolResult {
  return {
    content: [{
      type: "text",
      text: `zvec_grep_search for ${plan.root} was cancelled. No partial ranking is available because the index search is a single call; use Grep for an exact anchor or retry with a narrower query.`,
    }],
    details: { root: plan.root, cancelled: true },
    isError: true,
  };
}

/**
 * runCliSearch serves mode=external-daemon. The CLI owns the daemon lifecycle,
 * so this path refuses to run when the daemon is not ready instead of silently
 * starting one.
 */
async function runCliSearch(plan: SearchPlan, ctx: ToolContext): Promise<ToolResult> {
  if (!cliAvailable()) {
    return errorResult(`zg CLI not found at ${zgBinary()} — run the extension setup (bun run setup) first.`);
  }
  const ready = await runCli(["server", "status", "--check-ready"], plan.root);
  if (ready.code !== 0) {
    return errorResult(
      "zvec-grep external-daemon mode is selected but `zg server status --check-ready` failed. " +
        `Start the daemon with \`zg server on\` or switch the extension setting mode back to in-process.\n${tail(ready.stderr || ready.stdout)}`,
    );
  }
  const args = ["query", "--mode", "server", "--limit", String(plan.limit), "--preview", plan.preview];
  for (const query of plan.queries) args.push("--hybrid", query);
  for (const query of plan.fts) args.push("--fts", query);
  for (const query of plan.vector) args.push("--vector", query);
  if (plan.fuse) args.push("--fuse");
  if (plan.freshness === "eventual") args.push("--refresh", "off");
  for (const glob of plan.globs ?? []) args.push("-g", glob);
  for (const glob of plan.insensitiveGlobs ?? []) args.push("--iglob", glob);
  for (const type of plan.fileTypes ?? []) args.push("-t", type);
  for (const type of plan.excludedFileTypes ?? []) args.push("-T", type);
  if (plan.hidden) args.push("--hidden");
  if (plan.noIgnore) args.push("--no-ignore");
  const result = await runCli(args, plan.root);
  const text = (result.stdout || result.stderr).trim();
  if (result.code !== 0) {
    return {
      content: [{ type: "text", text: `zvec_grep_search (external-daemon) failed: ${tail(text)}` }],
      details: { root: plan.root, code: result.code, mode: ctx.config.mode },
      isError: true,
    };
  }
  return {
    content: [{ type: "text", text: tail(text, 60_000) }],
    details: { root: plan.root, source: "index-cli", mode: ctx.config.mode, limit: plan.limit },
  };
}

function errorResult(message: string): ToolResult {
  return { content: [{ type: "text", text: message }], isError: true };
}

function tail(text: string, max = 4_000): string {
  const trimmed = text.trim();
  return trimmed.length <= max ? trimmed : `…\n${trimmed.slice(trimmed.length - max)}`;
}

function rootOf(args: Record<string, unknown>, cwd: string): { path: string } | { error: string } {
  const raw = typeof args.root === "string" ? args.root.trim() : "";
  const candidate = raw || cwd || process.cwd();
  if (!isAbsolute(candidate)) return { error: `root must be an absolute path: ${candidate}` };
  try {
    if (!statSync(candidate).isDirectory()) return { error: `root is not a directory: ${candidate}` };
  } catch {
    return { error: `root does not exist: ${candidate}` };
  }
  return { path: candidate };
}

function isAbsolute(path: string): boolean {
  if (path.startsWith("/") || path.startsWith("\\\\")) return true;
  return /^[A-Za-z]:[\\/]/.test(path);
}

function stringList(value: unknown): string[] {
  if (typeof value === "string") return value.trim() ? [value.trim()] : [];
  if (!Array.isArray(value)) return [];
  return value.map((item) => (typeof item === "string" ? item.trim() : "")).filter(Boolean);
}

function optionalList(value: unknown): string[] | undefined {
  const list = stringList(value);
  return list.length ? list : undefined;
}

function optionalInteger(value: unknown, min: number): number | undefined {
  const number = Number(value);
  if (!Number.isFinite(number)) return undefined;
  const floored = Math.floor(number);
  return floored >= min ? floored : undefined;
}

function clampInteger(value: unknown, min: number, max: number, fallback: number): number {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.max(min, Math.min(max, Math.floor(number)));
}

/** modifiedTime accepts epoch milliseconds or an ISO date, like the CLI does. */
function modifiedTime(value: unknown): number | null | undefined {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value === "number" && Number.isFinite(value)) return Math.floor(value);
  if (typeof value === "string") {
    const parsed = Date.parse(value.trim());
    if (!Number.isNaN(parsed)) return parsed;
    return null;
  }
  return null;
}
