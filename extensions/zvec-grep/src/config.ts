import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

export type ZvecConfig = {
  embedding: string;
  device: "auto" | "cpu" | "metal" | "vulkan" | "cuda";
  mode: "in-process" | "external-daemon";
  maxResults: number;
  ignoredGlobs: string[];
  searchTimeoutMs: number;
  notifyOnIndexComplete: boolean;
};

export const DEFAULT_CONFIG: ZvecConfig = Object.freeze({
  embedding: "local/potion-code-16m-v2",
  device: "auto",
  mode: "in-process",
  maxResults: 5,
  ignoredGlobs: [],
  searchTimeoutMs: 90_000,
  notifyOnIndexComplete: false,
});

const DEVICES = ["auto", "cpu", "metal", "vulkan", "cuda"];
const MODES = ["in-process", "external-daemon"];

let cached: ZvecConfig | undefined;

function clone(config: ZvecConfig): ZvecConfig {
  return { ...config, ignoredGlobs: [...config.ignoredGlobs] };
}

export function extensionRoot(): string {
  return process.env.KI_EXTENSION_ROOT || process.cwd();
}

export function configPath(): string {
  return join(extensionRoot(), "config.json");
}

/**
 * The Host writes extension settings into the package's own config.json, so the
 * file stays the single source of truth: `config.updated` only invalidates this
 * cache.
 */
export function loadConfig(): ZvecConfig {
  if (cached) return clone(cached);
  const path = configPath();
  let saved: unknown = {};
  if (existsSync(path)) {
    try {
      saved = JSON.parse(readFileSync(path, "utf8"));
    } catch (error) {
      throw new Error(`zvec-grep config.json is invalid: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  cached = normalizeConfig(saved);
  return clone(cached);
}

export function invalidateConfig() {
  cached = undefined;
}

/**
 * isLocalEmbedding mirrors the library's model-reference rule (`provider/name`):
 * a device only applies to `local/*` models, and zg rejects `device`/`--device`
 * for every other provider.
 */
export function isLocalEmbedding(reference: string): boolean {
  const separator = reference.indexOf("/");
  return separator > 0 && reference.slice(0, separator) === "local";
}

export function normalizeConfig(input: unknown): ZvecConfig {
  const raw = input && typeof input === "object" && !Array.isArray(input) ? (input as Record<string, unknown>) : {};
  const embedding = typeof raw.embedding === "string" && raw.embedding.trim() ? raw.embedding.trim() : DEFAULT_CONFIG.embedding;
  const device = DEVICES.includes(String(raw.device)) ? (String(raw.device) as ZvecConfig["device"]) : DEFAULT_CONFIG.device;
  const mode = MODES.includes(String(raw.mode)) ? (String(raw.mode) as ZvecConfig["mode"]) : DEFAULT_CONFIG.mode;
  return {
    embedding,
    device,
    mode,
    maxResults: clampInteger(raw.maxResults, 1, 20, DEFAULT_CONFIG.maxResults),
    ignoredGlobs: stringList(raw.ignoredGlobs, 50),
    searchTimeoutMs: clampInteger(raw.searchTimeoutMs, 5_000, 115_000, DEFAULT_CONFIG.searchTimeoutMs),
    notifyOnIndexComplete: raw.notifyOnIndexComplete === true,
  };
}

function stringList(value: unknown, max: number): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") continue;
    const trimmed = item.trim();
    if (!trimmed || out.includes(trimmed)) continue;
    out.push(trimmed);
    if (out.length >= max) break;
  }
  return out;
}

function clampInteger(value: unknown, min: number, max: number, fallback: number): number {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.max(min, Math.min(max, Math.floor(number)));
}
