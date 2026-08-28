import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

const MAX_ENTRIES = 120;
const DEFAULT_TTL_MS = 10 * 60_000;

function hash(value) {
  return createHash("sha256").update(JSON.stringify(value)).digest("hex");
}

export class SearchCache {
  path: string;
  entries: Map<string, any>;
  loaded: boolean;
  writeChain: Promise<void>;

  constructor(root) {
    this.path = join(root || process.cwd(), "cache.json");
    this.entries = new Map();
    this.loaded = false;
    this.writeChain = Promise.resolve();
  }

  load() {
    if (this.loaded) return;
    this.loaded = true;
    if (!existsSync(this.path)) return;
    try {
      const raw = JSON.parse(readFileSync(this.path, "utf8"));
      for (const [key, value] of Object.entries(raw?.entries || {})) {
        if (value && typeof value === "object") this.entries.set(key, value);
      }
    } catch {
      this.entries.clear();
    }
  }

  key(query, options) {
    return hash({
      query,
      numResults: options.numResults,
      includeContent: options.includeContent,
      recencyFilter: options.recencyFilter || "",
      domainFilter: options.domainFilter || [],
      provider: options.provider || "all",
    });
  }

  get(key, ttlMs = DEFAULT_TTL_MS) {
    this.load();
    const entry = this.entries.get(key);
    if (!entry || Date.now() - Number(entry.createdAt || 0) > ttlMs) {
      if (entry) this.entries.delete(key);
      return null;
    }
    return structuredClone(entry.value);
  }

  put(key, value) {
    this.load();
    this.entries.set(key, { createdAt: Date.now(), value: structuredClone(value) });
    while (this.entries.size > MAX_ENTRIES) this.entries.delete(this.entries.keys().next().value);
    this.persist();
  }

  persist() {
    const document = { version: 1, entries: Object.fromEntries(this.entries) };
    this.writeChain = this.writeChain.then(async () => {
      await mkdir(dirname(this.path), { recursive: true, mode: 0o700 });
      const temp = `${this.path}.${process.pid}.tmp`;
      await writeFile(temp, `${JSON.stringify(document)}\n`, { mode: 0o600 });
      // A cache write may race with another instance. Losing a cache entry is
      // harmless; credentials are never stored in this file.
      await import("node:fs/promises").then(({ rename }) => rename(temp, this.path));
    }).catch(() => undefined);
  }

  response(key, value) {
    this.put(`response:${key}`, value);
  }

  responseValue(key) {
    const entry = this.get(`response:${key}`, 24 * 60 * 60_000);
    return entry;
  }
}

export function responseId(query, options) {
  return hash({ query, options, at: Math.floor(Date.now() / 60_000) });
}
