import assert from "node:assert/strict";
import { test } from "node:test";
import { parseCommand } from "./command.js";
import { MAX_OBJECTIVE_LENGTH } from "./prompts.js";

test("empty args show status", () => {
  assert.deepEqual(parseCommand(""), { kind: "show" });
  assert.deepEqual(parseCommand("   "), { kind: "show" });
  assert.deepEqual(parseCommand("status"), { kind: "show" });
});

test("verbs", () => {
  assert.deepEqual(parseCommand("pause"), { kind: "pause" });
  assert.deepEqual(parseCommand("resume"), { kind: "resume" });
  assert.deepEqual(parseCommand("clear"), { kind: "clear" });
  assert.deepEqual(parseCommand("stop"), { kind: "clear" });
});

test("start and edit keep the objective", () => {
  assert.deepEqual(parseCommand("fix the tests"), { kind: "start", objective: "fix the tests" });
  assert.deepEqual(parseCommand("edit ship the ui"), { kind: "edit", objective: "ship the ui" });
});

test("rejects extra tokens on verbs and empty edit", () => {
  assert.equal("error" in parseCommand("pause now"), true);
  assert.equal("error" in parseCommand("edit"), true);
  assert.equal("error" in parseCommand(""), false);
});

test("rejects oversized objectives", () => {
  const got = parseCommand("x".repeat(MAX_OBJECTIVE_LENGTH + 1));
  assert.equal("error" in got, true);
});
