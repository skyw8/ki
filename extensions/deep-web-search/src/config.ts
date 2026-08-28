import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

export const DEFAULT_CONFIG = Object.freeze({
  exaMode: "auto",
  codexModel: "gpt-5.5",
  provider: "all",
  providerToggles: Object.freeze({ codex: true, exa: true, tinyfish: true, duckduckgo: true }),
  maxResults: 5,
  fetchContent: false,
  summaryModel: "openai-codex/gpt-5.5",
  summaryGenerationDeadlineMs: 30_000,
  workflow: "none",
});

let cached;

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function merge(base, override) {
  const out = { ...base };
  for (const [key, value] of Object.entries(override ?? {})) {
    if (value && typeof value === "object" && !Array.isArray(value) && base?.[key] && typeof base[key] === "object") {
      out[key] = merge(base[key], value);
    } else {
      out[key] = value;
    }
  }
  return out;
}

export function configPath() {
  return join(process.env.KI_EXTENSION_ROOT || process.cwd(), "config.json");
}

export function loadConfig() {
  if (cached) return clone(cached);
  const path = configPath();
  let saved = {};
  if (existsSync(path)) {
    try {
      const parsed = JSON.parse(readFileSync(path, "utf8"));
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) saved = parsed;
    } catch (error) {
      throw new Error(`deep-web-search config is invalid: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  cached = merge(DEFAULT_CONFIG, saved);
  cached.providerToggles = merge(DEFAULT_CONFIG.providerToggles, cached.providerToggles);
  return clone(cached);
}

export function invalidateConfig() {
  cached = undefined;
}

export function normalizeConfig(config) {
  const next = merge(DEFAULT_CONFIG, config ?? {});
  next.providerToggles = merge(DEFAULT_CONFIG.providerToggles, next.providerToggles);
  next.maxResults = clampInteger(next.maxResults, 1, 20, 5);
  next.summaryGenerationDeadlineMs = clampInteger(next.summaryGenerationDeadlineMs, 1_000, 120_000, 30_000);
  if (!["auto", "all", "codex", "exa", "tinyfish", "duckduckgo"].includes(next.provider)) next.provider = "all";
  if (!["auto", "api", "mcp"].includes(next.exaMode)) next.exaMode = "auto";
  if (!["none", "auto-summary"].includes(next.workflow)) next.workflow = "none";
  return next;
}

function clampInteger(value, min, max, fallback) {
  if (!Number.isFinite(Number(value))) return fallback;
  return Math.max(min, Math.min(max, Math.floor(Number(value))));
}

export function configuredApiKey(config, field, envName) {
  const value = typeof config?.[field] === "string" ? config[field].trim() : "";
  if (value && value !== "<configured>") return value;
  const environment = typeof process.env[envName] === "string" ? process.env[envName].trim() : "";
  return environment || null;
}
