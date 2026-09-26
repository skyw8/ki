import { execFile, spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { extensionRoot } from "./config.js";

export type CliResult = {
  code: number | null;
  stdout: string;
  stderr: string;
  error?: string;
};

export type CliProgress = {
  phase: "scanning" | "model" | "indexing" | "done";
  model?: string;
  done?: number;
  total?: number;
  failed?: number;
  detail?: string;
};

/**
 * zgBinary prefers the package-local CLI installed by `bun run setup` so the
 * extension never depends on a user-installed zg. KI_ZVEC_GREP_CLI is a test
 * seam for the command tests.
 */
export function zgBinary(): string {
  const override = (process.env.KI_ZVEC_GREP_CLI || "").trim();
  if (override) return override;
  const name = process.platform === "win32" ? "zg.cmd" : "zg";
  return join(extensionRoot(), "node_modules", ".bin", name);
}

export function cliAvailable(): boolean {
  return existsSync(zgBinary());
}

const CLI_TIMEOUT_MS = 30_000;

/** runCli executes one short zg command (status, drop, daemon checks). */
export function runCli(args: string[], cwd: string): Promise<CliResult> {
  return new Promise<CliResult>((resolve) => {
    execFile(zgBinary(), args, { cwd, timeout: CLI_TIMEOUT_MS, maxBuffer: 4 * 1024 * 1024 }, (error, stdout, stderr) => {
      const failure = error as (Error & { code?: number | string; killed?: boolean }) | null;
      resolve({
        code: typeof failure?.code === "number" ? failure.code : failure ? null : 0,
        stdout: String(stdout ?? ""),
        stderr: String(stderr ?? ""),
        error: failure ? failure.message : undefined,
      });
    });
  });
}

export type IndexRun = {
  done: Promise<CliResult>;
  cancel: () => void;
};

/**
 * runIndex streams `zg index` progress. Indexing is deliberately kept on the
 * CLI: the first run may download an embedding model, far beyond the Host's
 * 120s tool budget, so it runs detached from any tool call.
 */
export function runIndex(root: string, args: string[], onProgress: (progress: CliProgress) => void): IndexRun {
  const child = spawn(zgBinary(), ["index", root, ...args], { cwd: root, stdio: ["ignore", "pipe", "pipe"] });
  const stdout: string[] = [];
  const stderr: string[] = [];
  let tail = "";
  const consume = (chunk: Buffer, sink: string[]) => {
    const text = chunk.toString("utf8");
    sink.push(text);
    tail += text;
    const lines = tail.split("\n");
    tail = lines.pop() ?? "";
    for (const line of lines) {
      const progress = parseProgressLine(line);
      if (progress) onProgress(progress);
    }
  };
  child.stdout?.on("data", (chunk: Buffer) => consume(chunk, stdout));
  child.stderr?.on("data", (chunk: Buffer) => consume(chunk, stderr));
  const done = new Promise<CliResult>((resolve) => {
    child.on("error", (error) => {
      resolve({ code: null, stdout: stdout.join(""), stderr: stderr.join(""), error: error.message });
    });
    child.on("close", (code) => {
      resolve({ code, stdout: stdout.join(""), stderr: stderr.join("") });
    });
  });
  return {
    done,
    cancel: () => {
      child.kill("SIGTERM");
    },
  };
}

/** parseProgressLine understands the non-TTY progress lines zg prints. */
export function parseProgressLine(line: string): CliProgress | undefined {
  const text = line.trim();
  if (!text) return undefined;
  if (/^scanning files/i.test(text)) return { phase: "scanning", detail: text };
  const download = /^downloading (.+?)(?: ·.*)?$/i.exec(text);
  if (download) return { phase: "model", model: download[1] };
  const preparing = /^preparing (.+)$/i.exec(text);
  if (preparing) return { phase: "model", model: preparing[1] };
  const indexing = /^indexing files:?\s*(\d+)\/(\d+)(.*)$/i.exec(text);
  if (indexing) {
    const failed = /(\d+)\s+failed/.exec(indexing[3] ?? "");
    return {
      phase: "indexing",
      done: Number(indexing[1]),
      total: Number(indexing[2]),
      failed: failed ? Number(failed[1]) : 0,
    };
  }
  if (/^indexing (complete|finished)/i.test(text)) return { phase: "done", detail: text };
  return undefined;
}

/** summaryFields extracts the tab-separated summary lines of an index run. */
export function summaryFields(stdout: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of stdout.split("\n")) {
    const match = /^([a-z_]+)\t(.*)$/.exec(line.trim());
    if (match) out[match[1]] = match[2];
  }
  return out;
}
