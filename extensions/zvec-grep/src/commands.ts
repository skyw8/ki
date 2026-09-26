import { resolve } from "node:path";
import { cliAvailable, runCli, zgBinary } from "./cli.js";
import { isLocalEmbedding, loadConfig, type ZvecConfig } from "./config.js";
import type { Host } from "./host.js";
import type { IndexJobs } from "./index-job.js";

export type CommandSpec = {
  name: string;
  description: string;
  argumentHint?: string;
};

export const COMMANDS: CommandSpec[] = [
  {
    name: "zg-index",
    description: "Build or update the zvec-grep index for a workspace",
    argumentHint: "[root] [--rebuild] [zg index options]",
  },
  {
    name: "zg-status",
    description: "Show the zvec-grep index status for a workspace",
    argumentHint: "[root]",
  },
  {
    name: "zg-remove",
    description: "Delete the zvec-grep index of a workspace",
    argumentHint: "[root]",
  },
];

export const COMMAND_NAMES = COMMANDS.map((command) => command.name);

export type CommandDeps = {
  cwd: string;
  host: Host;
  jobs: IndexJobs;
};

export type CommandOutcome = { handled: boolean; notice: string };

type Parsed = { root: string; extraArgs: string[]; error?: string };

/**
 * parseTarget accepts `[root] [zg index options]`. The first bare token is the
 * workspace root (relative to the session cwd); option tokens are passed
 * through to `zg index` with their value.
 */
export function parseTarget(args: string, cwd: string, allowOptions: boolean): Parsed {
  const tokens = args.trim() ? args.trim().split(/\s+/) : [];
  let root = "";
  const extraArgs: string[] = [];
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token.startsWith("-")) {
      if (!allowOptions) return { root: "", extraArgs: [], error: `${token} is not supported here` };
      if (token === "--drop" || token === "--yes") {
        return { root: "", extraArgs: [], error: "use /zg-remove to delete an index; --drop is not accepted here" };
      }
      extraArgs.push(token);
      const next = tokens[index + 1];
      if (!token.includes("=") && next && !next.startsWith("-")) {
        extraArgs.push(next);
        index += 1;
      }
      continue;
    }
    if (!root) {
      root = token;
      continue;
    }
    return { root: "", extraArgs: [], error: `unexpected argument: ${token}` };
  }
  const absolute = resolve(cwd || process.cwd(), root || ".");
  return { root: absolute, extraArgs };
}

export async function invokeCommand(name: string, args: string, deps: CommandDeps): Promise<CommandOutcome> {
  switch (name) {
    case "zg-status":
      return statusCommand(args, deps);
    case "zg-index":
      return indexCommand(args, deps);
    case "zg-remove":
      return removeCommand(args, deps);
    default:
      return { handled: false, notice: "" };
  }
}

async function statusCommand(args: string, deps: CommandDeps): Promise<CommandOutcome> {
  const parsed = parseTarget(args, deps.cwd, false);
  if (parsed.error) return { handled: true, notice: parsed.error };
  const lines: string[] = [];
  const job = deps.jobs.get(deps.host.sessionId);
  if (job) lines.push(indexJobLine(job));
  if (!cliAvailable()) {
    lines.push(`zg CLI not found (${zgBinary()}). Run \`bun run setup\` in the extension directory.`);
    return { handled: true, notice: lines.join("\n") };
  }
  const result = await runCli(["status", parsed.root, "--mode", "direct"], parsed.root);
  const text = (result.stdout || result.stderr).trim();
  lines.push(text || `zg status failed (exit ${result.code}).`);
  return { handled: true, notice: lines.join("\n") };
}

function indexCommand(args: string, deps: CommandDeps): CommandOutcome {
  const parsed = parseTarget(args, deps.cwd, true);
  if (parsed.error) return { handled: true, notice: parsed.error };
  if (!cliAvailable()) {
    return { handled: true, notice: `zg CLI not found (${zgBinary()}). Run \`bun run setup\` in the extension directory.` };
  }
  const rebuild = parsed.extraArgs.includes("--rebuild");
  const extraArgs = parsed.extraArgs.filter((token) => token !== "--rebuild");
  const config = currentConfig();
  const started = deps.jobs.start(
    deps.host.sessionId,
    { root: parsed.root, rebuild, settingArgs: settingArgs(config, extraArgs), extraArgs },
    deps.host,
    config,
  );
  return { handled: true, notice: started.notice };
}

/**
 * settingArgs forwards the configured embedding model and device to `zg index`,
 * so a new index is built with the model the sidecar also embeds queries with;
 * without this the index would follow zg's own global default while queries
 * followed the extension setting, and the two could disagree.
 *
 * Flags the user typed in the command win, so one run can still override either
 * value (`/zg-index --embedding local/other`). Upstream refuses to change the
 * model of an existing index without `--rebuild`, so a changed setting surfaces
 * as a rebuild hint instead of a silently mixed index.
 */
function settingArgs(config: ZvecConfig, extraArgs: string[]): string[] {
  const typed = new Set(extraArgs.map(flagName));
  const args: string[] = [];
  if (config.embedding && !typed.has("--embedding")) args.push("--embedding", config.embedding);
  // `zg index` rejects a device for a remote model, so it only travels with a
  // `local/*` reference; the query path applies the same rule.
  if (config.device && isLocalEmbedding(config.embedding) && !typed.has("--device")) args.push("--device", config.device);
  return args;
}

/** flagName strips a `--flag=value` token down to its flag. */
function flagName(token: string): string {
  const separator = token.indexOf("=");
  return separator < 0 ? token : token.slice(0, separator);
}

async function removeCommand(args: string, deps: CommandDeps): Promise<CommandOutcome> {
  const parsed = parseTarget(args, deps.cwd, false);
  if (parsed.error) return { handled: true, notice: parsed.error };
  if (deps.jobs.get(deps.host.sessionId)) {
    return { handled: true, notice: "An index job is running for this session; wait for it to finish before removing the index." };
  }
  if (!cliAvailable()) {
    return { handled: true, notice: `zg CLI not found (${zgBinary()}). Run \`bun run setup\` in the extension directory.` };
  }
  const ok = await deps.host.confirm(
    { key: "confirm.drop.title", fallback: "Remove the zvec-grep index?" },
    { key: "confirm.drop.message", params: { root: parsed.root }, fallback: `This deletes ${parsed.root}/.zvec-grep.` },
  );
  if (!ok) return { handled: true, notice: "Cancelled; the index was not removed." };
  const result = await runCli(["index", parsed.root, "--drop", "--yes", "--mode", "direct"], parsed.root);
  const text = (result.stdout || result.stderr).trim();
  if ((result.code ?? 1) !== 0) return { handled: true, notice: `zg index --drop failed (exit ${result.code}):\n${text}` };
  return { handled: true, notice: text || `Removed the zvec-grep index for ${parsed.root}.` };
}

function indexJobLine(job: { root: string; lastProgress?: { phase: string; done?: number; total?: number } }): string {
  const progress = job.lastProgress;
  if (!progress) return `Index job running for ${job.root}.`;
  if (progress.phase === "indexing" && progress.total) {
    return `Index job running for ${job.root}: ${progress.done ?? 0}/${progress.total} files.`;
  }
  return `Index job running for ${job.root} (${progress.phase}).`;
}

function currentConfig(): ZvecConfig {
  try {
    return loadConfig();
  } catch {
    // A broken config.json must not block index management: the job only needs
    // the notify flag, so fall back to the defaults.
    return {
      embedding: "local/potion-code-16m-v2",
      device: "auto",
      mode: "in-process",
      maxResults: 5,
      ignoredGlobs: [],
      searchTimeoutMs: 90_000,
      notifyOnIndexComplete: false,
    };
  }
}
