// @ts-nocheck
import { readFileSync } from "node:fs";
// Configuration loading for freerouter.
//
// The host stores extension settings in {extensionRoot}/config.json (secrets
// included; the host never returns real secret values over HTTP). The sidecar
// reads this file at startup and again on every config.updated notification.
// env/credential resolution order for the API key is decided by the caller:
// host credential → config.json apiKey → OPENROUTER_API_KEY env.
export const DEFAULTS = Object.freeze({
    raceWidth: 2,
    maxBatches: 3,
    exhaustedTtlMs: 90000,
    slowTtlMs: 15000,
    firstTokenTimeoutMs: 10000,
    idleTimeoutMs: 30000,
    refreshIntervalMs: 3600000,
    apiKey: "",
    baseUrl: "https://openrouter.ai/api/v1",
});
function clampInt(value, fallback, min, max) {
    const n = Number(value);
    if (!Number.isFinite(n))
        return fallback;
    return Math.min(max, Math.max(min, Math.round(n)));
}
/**
 * Read and normalize the extension's private config. Always returns a full
 * object; malformed files fall back to defaults rather than breaking the
 * sidecar (fail-open matches ki's extension failure strategy).
 */
export function readConfig(root, env = process.env) {
    let raw = {};
    try {
        raw = JSON.parse(readFileSync(`${root}/config.json`, "utf8"));
    }
    catch {
        raw = {};
    }
    if (raw === null || typeof raw !== "object" || Array.isArray(raw))
        raw = {};
    const apiKey = typeof raw.apiKey === "string" ? raw.apiKey : DEFAULTS.apiKey;
    const baseUrl = (typeof raw.baseUrl === "string" && raw.baseUrl.trim()) ||
        (env.OPENROUTER_BASE_URL || "").trim() ||
        DEFAULTS.baseUrl;
    return {
        raceWidth: clampInt(raw.raceWidth, DEFAULTS.raceWidth, 1, 8),
        maxBatches: clampInt(raw.maxBatches, DEFAULTS.maxBatches, 1, 6),
        exhaustedTtlMs: clampInt(raw.exhaustedTtlMs, DEFAULTS.exhaustedTtlMs, 1000, 30 * 60_000),
        slowTtlMs: clampInt(raw.slowTtlMs, DEFAULTS.slowTtlMs, 1000, 10 * 60_000),
        firstTokenTimeoutMs: clampInt(raw.firstTokenTimeoutMs, DEFAULTS.firstTokenTimeoutMs, 1000, 5 * 60_000),
        idleTimeoutMs: clampInt(raw.idleTimeoutMs, DEFAULTS.idleTimeoutMs, 1000, 10 * 60_000),
        refreshIntervalMs: clampInt(raw.refreshIntervalMs, DEFAULTS.refreshIntervalMs, 60_000, 24 * 60 * 60_000),
        apiKey,
        baseUrl: baseUrl.replace(/\/+$/, ""),
    };
}
