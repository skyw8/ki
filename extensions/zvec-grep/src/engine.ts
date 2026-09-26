import { pathToFileURL } from "node:url";
import { isLocalEmbedding, type ZvecConfig } from "./config.js";
import { safeError } from "./rpc.js";

/** The subset of the library's result types this extension consumes. */
export type ContextRange = {
  kind?: string;
  startLine: number;
  endLine: number;
};

export type ContextItem = {
  kind: string;
  rank: number;
  file: { absolutePath: string; relativePath: string; rootPath?: string };
  range: ContextRange;
  excerptRange?: ContextRange;
  content: string;
  contentRole?: "source" | "outline";
  outline?: string;
  status: "fresh" | "possibly_stale";
  score?: number;
  matchedBy: string;
  metadata?: Record<string, unknown>;
  container?: { entityId: string; range: ContextRange; metadata?: Record<string, unknown> };
  queryGroups?: ReadonlyArray<{ id: string; query: string; role: string; rank: number; matchedBy: string }>;
};

export type ContextDiagnostics = {
  emptyReason?: "no_matches" | "no_searchable_files";
  index?: {
    hitsReturned: number;
    routes: ReadonlyArray<{ id: string; mode: "fts" | "vector"; query: string }>;
  };
  rg?: { backend: string; command: string; truncated: boolean; limit?: number };
  structure?: { truncated: boolean; enrichedItems: number; skippedFiles: number };
  timings?: ReadonlyArray<{ name: string; durationMs: number }>;
};

export type ContextResult = {
  query: string;
  root: string;
  source: "index" | "rg";
  coverage: "ranked_sample" | "rg_exhaustive" | "rg_truncated";
  workspaceIndex?: { id: string; name: string; path: string };
  items: ContextItem[];
  groupResults?: ReadonlyArray<{ id: string; query: string; role: string; items: ContextItem[] }>;
  diagnostics: ContextDiagnostics;
};

export type IndexResult = {
  root: string;
  files?: number;
  entities?: number;
  durationMs?: number;
};

export type InfoResult = {
  root: string;
  indexed: boolean;
  indexPolicy: string;
  home: string;
  indexPath: string;
  source: "index" | "unindexed";
  suggestion?: string;
};

/** Structural view of the @zvec/zvec-grep instance used by this sidecar. */
export type ZvecGrepEngine = {
  readonly root: string;
  info(options?: { root?: string; includeStatus?: boolean }): Promise<InfoResult>;
  context(options: Record<string, unknown>): Promise<ContextResult>;
  index(options?: Record<string, unknown>): Promise<IndexResult>;
  dropIndex(options?: { root?: string }): Promise<boolean>;
  close(): Promise<void>;
};

export type ZvecGrepLibrary = {
  createZvecGrep(options?: Record<string, unknown>): Promise<ZvecGrepEngine>;
};

let libraryPromise: Promise<ZvecGrepLibrary> | undefined;

/**
 * Loads the search library. KI_ZVEC_GREP_LIB is a test seam: the runtime tests
 * point it at a stub module so the sidecar's protocol behavior can be exercised
 * without the native package or a built index.
 */
export function loadLibrary(): Promise<ZvecGrepLibrary> {
  libraryPromise ??= (async () => {
    const override = (process.env.KI_ZVEC_GREP_LIB || "").trim();
    const loaded = override
      ? ((await import(pathToFileURL(override).href)) as unknown)
      : ((await import("@zvec/zvec-grep")) as unknown);
    const library = loaded as Partial<ZvecGrepLibrary>;
    if (typeof library.createZvecGrep !== "function") {
      throw new Error(`${override || "@zvec/zvec-grep"} does not export createZvecGrep`);
    }
    return library as ZvecGrepLibrary;
  })();
  return libraryPromise;
}

/** SearchTimeoutError marks a search that outlived the sidecar budget. */
export class SearchTimeoutError extends Error {
  constructor(ms: number) {
    super(`search exceeded its ${ms}ms budget`);
    this.name = "SearchTimeoutError";
  }
}

/** SearchCancelledError marks a search the Host asked to abandon. */
export class SearchCancelledError extends Error {
  constructor() {
    super("search cancelled");
    this.name = "SearchCancelledError";
  }
}

const MAX_CONCURRENT_SEARCHES = 4;

/**
 * EnginePool keeps one library instance per effective configuration and bounds
 * how many searches run at once.
 *
 * Why a bound instead of a strict queue: the library exposes no cancellation, so
 * one wedged search must not block every later one. A search that outlives its
 * budget releases its slot and is left to the library's own workspace locks,
 * which are shared for reads.
 */
export class EnginePool {
  private engine?: { key: string; value: ZvecGrepEngine };
  private creating?: Promise<ZvecGrepEngine>;
  private active = 0;
  private readonly waiters: Array<() => void> = [];

  private static key(config: ZvecConfig): string {
    return JSON.stringify({ embedding: config.embedding, device: config.device });
  }

  private async acquireEngine(config: ZvecConfig): Promise<ZvecGrepEngine> {
    const key = EnginePool.key(config);
    if (this.engine?.key === key) return this.engine.value;
    if (this.creating) return this.creating;
    await this.dispose();
    this.creating = (async () => {
      const library = await loadLibrary();
      // A device only travels with local models: zg rejects it for a remote
      // provider ("device is only supported for local embedding models"), so a
      // remote embedding setting must not fail the search.
      const engine = await library.createZvecGrep({
        embedding: config.embedding,
        device: isLocalEmbedding(config.embedding) ? config.device : undefined,
      });
      this.engine = { key, value: engine };
      return engine;
    })();
    try {
      return await this.creating;
    } finally {
      this.creating = undefined;
    }
  }

  private async slot(deadlineMs: number): Promise<() => void> {
    if (this.active < MAX_CONCURRENT_SEARCHES) {
      this.active += 1;
      return () => this.release();
    }
    await withDeadline(new Promise<void>((resolve) => this.waiters.push(resolve)), deadlineMs);
    this.active += 1;
    return () => this.release();
  }

  private release() {
    this.active -= 1;
    const next = this.waiters.shift();
    if (next) next();
  }

  /** run performs one engine operation under the concurrency bound. */
  async run<T>(config: ZvecConfig, deadlineMs: number, operation: (engine: ZvecGrepEngine) => Promise<T>): Promise<T> {
    const release = await this.slot(deadlineMs);
    try {
      const engine = await this.acquireEngine(config);
      return await withDeadline(operation(engine), deadlineMs);
    } finally {
      release();
    }
  }

  /** dispose drops the current engine; the next run reloads the library. */
  async dispose(): Promise<void> {
    const engine = this.engine;
    this.engine = undefined;
    if (!engine) return;
    try {
      await engine.value.close();
    } catch (error) {
      process.stderr.write(`zvec-grep engine close: ${safeError(error)}\n`);
    }
  }
}

/** withDeadline rejects with SearchTimeoutError; it cannot abort the callee. */
export function withDeadline<T>(promise: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new SearchTimeoutError(ms)), ms);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

export type EngineErrorInfo = { code: string; message: string; details: string };

/** Normalizes a library failure so the tool can answer with actionable text. */
export function engineError(error: unknown): EngineErrorInfo {
  const record = error && typeof error === "object" ? (error as Record<string, unknown>) : {};
  const code = typeof record.code === "string" ? record.code : "";
  const details = typeof record.context === "string" ? record.context : "";
  return { code, message: safeError(error), details };
}
