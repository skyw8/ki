import type { ZvecConfig } from "./config.js";
import { type CliProgress, runIndex, summaryFields } from "./cli.js";
import type { Host } from "./host.js";

const STATUS_KEY = "zgIndex";

export type IndexJobState = {
  root: string;
  startedAt: number;
  lastProgress?: CliProgress;
  finished?: { ok: boolean; text: string; files?: number; entities?: number; seconds?: number };
};

export type IndexRequest = {
  root: string;
  rebuild?: boolean;
  /** Settings-derived flags the caller puts before the user's own flags. */
  settingArgs: string[];
  extraArgs: string[];
};

/**
 * IndexJobs owns the long-running `zg index` children, one per session.
 *
 * Why the CLI and not the library: a first index can download an embedding
 * model, which is unbounded work that must not run inside a tool call. The
 * child is detached from the sidebar's RPC handling, and progress reaches the
 * UI through ui.setStatus.
 */
export class IndexJobs {
  private readonly jobs = new Map<string, { run: ReturnType<typeof runIndex>; state: IndexJobState }>();

  constructor(private readonly onFinished: (sessionId: string, root: string, state: IndexJobState, config: ZvecConfig) => void) {}

  get(sessionId: string): IndexJobState | undefined {
    return this.jobs.get(sessionId)?.state;
  }

  start(sessionId: string, request: IndexRequest, host: Host, config: ZvecConfig): { started: boolean; notice: string } {
    if (this.jobs.has(sessionId)) {
      const state = this.jobs.get(sessionId)!.state;
      return { started: false, notice: `An index job is already running for ${state.root}.` };
    }
    const args = [...request.settingArgs, ...request.extraArgs, "--mode", "direct"];
    if (request.rebuild) args.push("--rebuild");
    const state: IndexJobState = { root: request.root, startedAt: Date.now() };
    const run = runIndex(request.root, args, (progress) => {
      state.lastProgress = progress;
      void this.report(host, progress);
    });
    this.jobs.set(sessionId, { run, state });
    void host.setStatus(STATUS_KEY, { key: "index.started", params: { root: request.root }, fallback: `Index job started for ${request.root}` });
    run.done.then((result) => {
      this.jobs.delete(sessionId);
      const ok = result.code === 0;
      const summary = summaryFields(result.stdout);
      state.finished = {
        ok,
        text: (ok ? result.stdout : result.stderr || result.stdout).trim(),
        files: numberFrom(summary.files),
        entities: numberFrom(summary.entities),
        seconds: numberFrom(summary.duration),
      };
      if (ok) {
        void host.setStatus(STATUS_KEY, doneText(state), "success");
        setTimeout(() => void host.clearStatus(STATUS_KEY), 30_000);
      } else {
        void host.setStatus(STATUS_KEY, failedText(state), "error");
        setTimeout(() => void host.clearStatus(STATUS_KEY), 120_000);
      }
      this.onFinished(sessionId, request.root, state, config);
    });
    return {
      started: true,
      // Name the effective flags: the embedding/device settings are forwarded
      // silently, so the notice is where the user sees which model is built.
      notice: `Started \`zg index ${[request.root, ...request.settingArgs, ...request.extraArgs, request.rebuild ? "--rebuild" : ""].filter(Boolean).join(" ")}\` in the background. Progress appears on the extension status chip.`,
    };
  }

  cancel(sessionId: string): boolean {
    const job = this.jobs.get(sessionId);
    if (!job) return false;
    job.run.cancel();
    return true;
  }

  private async report(host: Host, progress: CliProgress) {
    if (progress.phase === "scanning") {
      await host.setStatus(STATUS_KEY, { key: "index.scanning", fallback: "Scanning files…" });
      return;
    }
    if (progress.phase === "model") {
      await host.setStatus(STATUS_KEY, {
        key: "index.modelDownload",
        params: { model: progress.model ?? "local" },
        fallback: `Preparing embedding model ${progress.model ?? ""}`.trim(),
      });
      return;
    }
    if (progress.phase === "indexing" && progress.total) {
      await host.setStatus(STATUS_KEY, {
        key: "index.progress",
        params: { done: progress.done ?? 0, total: progress.total, failed: progress.failed ?? 0 },
        fallback: `Indexing ${progress.done ?? 0}/${progress.total} files`,
      });
    }
  }
}

export function doneText(state: IndexJobState): { key: string; params: Record<string, string | number>; fallback: string } {
  const seconds = state.finished?.seconds ?? Math.round((Date.now() - state.startedAt) / 1000);
  const params = { root: state.root, files: state.finished?.files ?? 0, entities: state.finished?.entities ?? 0, seconds };
  return { key: "index.done", params, fallback: `Index ready for ${state.root}: ${params.files} files, ${params.entities} entities in ${seconds}s` };
}

export function failedText(state: IndexJobState): { key: string; params: Record<string, string | number>; fallback: string } {
  const raw = (state.finished?.text ?? "unknown error").split("\n").slice(-3).join(" ");
  const error = withRebuildHint(raw);
  return { key: "index.failed", params: { root: state.root, error }, fallback: `Index failed for ${state.root}: ${error}` };
}

/**
 * withRebuildHint names the command the user has when the failure is zg's
 * refusal to change the model of an existing index ("Re-run with --rebuild to
 * change the embedding model", or the library's `zg index --rebuild` hint).
 */
function withRebuildHint(error: string): string {
  if (!/--rebuild/.test(error) || !/embedding/i.test(error)) return error;
  return `${error} Use /zg-index --rebuild to rebuild it here.`;
}

/** notifyText is the plain-text session message for a finished index job. */
export function notifyText(state: IndexJobState): string {
  const files = state.finished?.files ?? 0;
  return `zvec-grep: the index for ${state.root} is ready (${files} files). zvec_grep_search can now answer semantic questions about it.`;
}

function numberFrom(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const match = /(\d+)/.exec(value);
  return match ? Number(match[1]) : undefined;
}
