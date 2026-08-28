import { codexAvailable, searchCodex } from "./codex.js";
import { exaAvailable, searchExa } from "./exa.js";
import { duckduckgoAvailable, searchDuckduckgo } from "./duckduckgo.js";
import { searchTinyfish, tinyfishAvailable } from "./tinyfish.js";

export const PROVIDERS = ["codex", "exa", "tinyfish", "duckduckgo"];

export function enabledProviders(config, requested) {
  const toggles = config.providerToggles || {};
  const requestedList = Array.isArray(requested) ? requested : requested === "all" || requested === "auto" ? PROVIDERS : [requested];
  const normalized = requestedList.map((value) => value === "ddg" ? "duckduckgo" : value).filter((value) => PROVIDERS.includes(value));
  const unique = [...new Set(normalized)];
  const selected = unique.length ? unique : PROVIDERS;
  return selected.filter((provider) => toggles[provider] !== false);
}

export function providerAvailability(name, config) {
  if (name === "codex") return codexAvailable();
  if (name === "exa") return exaAvailable(config);
  if (name === "tinyfish") return tinyfishAvailable(config);
  if (name === "duckduckgo") return duckduckgoAvailable();
  return false;
}

export function searchProvider(name, query, options, config, signal) {
  if (name === "codex") return searchCodex(query, { ...options, codexModel: config.codexModel }, signal);
  if (name === "exa") return searchExa(query, options, config, signal);
  if (name === "tinyfish") return searchTinyfish(query, options, config, signal);
  if (name === "duckduckgo") return searchDuckduckgo(query, options, signal);
  throw new Error(`unknown provider ${name}`);
}

export { fetchTinyfish, tinyfishAvailable } from "./tinyfish.js";
export { completeWithModel } from "./codex.js";
