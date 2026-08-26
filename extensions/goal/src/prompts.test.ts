import assert from "node:assert/strict";
import { test } from "node:test";
import {
  appendSystem,
  buildContinuePrompt,
  buildGoalPrompt,
  buildGoalSystemPrompt,
  buildObjectiveUpdatedPrompt,
  escapeXmlText,
  type GoalPromptContext,
} from "./prompts.js";
import { MIN_GOAL_WAIT_DELAY_MS } from "./wait.js";

const goal: GoalPromptContext = {
  id: "abc",
  text: "do <it>",
  status: "active",
  iteration: 3,
  tokensUsed: 0,
  startedAt: 0,
  updatedAt: 0,
  timeUsedSeconds: 0,
  baselineTokens: 0,
};

test("escapeXmlText", () => {
  assert.equal(escapeXmlText("a<b>&c"), "a&lt;b&gt;&amp;c");
});

test("pi-goal kickoff, system, continue, and edit wording", () => {
  const kickoff = buildGoalPrompt(goal);
  assert.match(kickoff, /^Goal mode is active\. Complete this goal fully:/);
  assert.match(kickoff, /<goal_objective>\ndo &lt;it&gt;\n<\/goal_objective>/);
  assert.match(kickoff, /<goal_id>\nabc\n<\/goal_id>/);
  assert.match(kickoff, /goal_complete tool stale-turn guard/);
  assert.match(kickoff, /narrower, safer, smaller, merely compatible, or easier-to-test/);
  assert.match(kickoff, /goal_wait/);
  assert.match(kickoff, new RegExp(String(MIN_GOAL_WAIT_DELAY_MS)));

  const system = buildGoalSystemPrompt(goal);
  assert.match(system, /^Active \/goal:/);
  assert.doesNotMatch(system, /Token budget/);

  const cont = buildContinuePrompt(goal, "marker-1");
  assert.match(cont, /This is automatic continuation #3/);
  assert.match(cont, /<!-- goal-continuation:marker-1 -->/);

  const edit = buildObjectiveUpdatedPrompt(goal);
  assert.match(edit, /supersedes every previous goal objective/);
});

test("appendSystem keeps the host system text", () => {
  assert.equal(appendSystem("base", "addon"), "base\n\naddon");
  assert.equal(appendSystem("", "addon"), "addon");
});
