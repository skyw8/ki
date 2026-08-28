import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DEFAULTS, readConfig } from "../dist/config.js";

test("readConfig returns defaults for a missing file", () => {
  const root = mkdtempSync(join(tmpdir(), "ki-fr-"));
  const config = readConfig(root, {});
  assert.equal(config.raceWidth, DEFAULTS.raceWidth);
  assert.equal(config.maxBatches, DEFAULTS.maxBatches);
  assert.equal(config.baseUrl, "https://openrouter.ai/api/v1");
  assert.equal(config.apiKey, "");
});

test("readConfig reads config.json and clamps raceWidth to 1..8", () => {
  const root = mkdtempSync(join(tmpdir(), "ki-fr-"));
  writeFileSync(join(root, "config.json"), JSON.stringify({ raceWidth: 99, apiKey: "sk-or-x" }));
  const config = readConfig(root, {});
  assert.equal(config.raceWidth, 8);
  assert.equal(config.apiKey, "sk-or-x");
  assert.equal(readConfig(join(root, "nope"), { OPENROUTER_BASE_URL: "http://x" }).baseUrl, "http://x");
});

test("readConfig raceWidth=1 is allowed (serial fallback mode)", () => {
  const root = mkdtempSync(join(tmpdir(), "ki-fr-"));
  writeFileSync(join(root, "config.json"), JSON.stringify({ raceWidth: 1 }));
  assert.equal(readConfig(root, {}).raceWidth, 1);
});

test("readConfig survives a malformed file", () => {
  const root = mkdtempSync(join(tmpdir(), "ki-fr-"));
  writeFileSync(join(root, "config.json"), "{not json");
  assert.equal(readConfig(root, {}).raceWidth, DEFAULTS.raceWidth);
});

test("baseUrl prefers config over env, trailing slashes trimmed", () => {
  const root = mkdtempSync(join(tmpdir(), "ki-fr-"));
  writeFileSync(join(root, "config.json"), JSON.stringify({ baseUrl: "http://cfg/base///" }));
  const config = readConfig(root, { OPENROUTER_BASE_URL: "http://env/base" });
  assert.equal(config.baseUrl, "http://cfg/base");
});
