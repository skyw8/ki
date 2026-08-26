import {
  MAX_GOAL_WAIT_DELAY_MS,
  MAX_GOAL_WAIT_REASON_LENGTH,
  MIN_GOAL_WAIT_DELAY_MS,
} from "./wait.js";

export const TOOL_COMPLETE = "goal_complete";
export const TOOL_BLOCKED = "goal_blocked";
export const TOOL_WAIT = "goal_wait";

export const MAX_GOAL_ID_LENGTH = 128;
export const MAX_COMPLETION_SUMMARY_LENGTH = 4_000;
export const MAX_BLOCKER_REASON_LENGTH = 1_000;
export const MAX_BLOCKER_EVIDENCE_LENGTH = 4_000;

const CONTRADICTORY_COMPLETION_PATTERNS = [
  /(?<!could\s)\bnot\s+(?:yet\s+)?(?:complete|completed|done|finished)\b/i,
  /\bstill\s+(?:incomplete|failing|failing\s+tests?|fails?)\b/i,
  /\btests?\s+(?:still\s+)?fail(?:ing)?\b/i,
];

export function isContradictoryCompletionSummary(summary: string) {
  return CONTRADICTORY_COMPLETION_PATTERNS.some((pattern) => pattern.test(summary));
}

export const TOOL_SPECS = [
  {
    name: TOOL_COMPLETE,
    description:
      "Mark the active /goal as complete after all required work is done and verified, using the current goal_id stale-turn guard. Do not use for partial progress, blockers, failing, or unverified work.",
    snippet:
      "Mark the active /goal as complete after fully finishing and verifying it, with the current goal_id",
    parameters: {
      type: "object",
      properties: {
        goal_id: {
          type: "string",
          description:
            "The exact goal_id shown in the current active /goal prompt. Used only to reject stale completion calls from older turns.",
        },
        summary: {
          type: "string",
          description:
            "State what was completed and what evidence verified it. Do not use this tool to report partial progress, blockers, failures, or remaining work.",
        },
      },
      required: ["goal_id", "summary"],
    },
  },
  {
    name: TOOL_BLOCKED,
    description:
      "Stop the active /goal only at a true impasse after the same blocker recurs for at least three consecutive goal turns, with the current goal_id and concrete evidence that user or external action is required. Do not use for ordinary clarification, uncertainty, or recoverable failures.",
    snippet: "Mark the active /goal blocked only after the same blocker recurs for three consecutive goal turns",
    parameters: {
      type: "object",
      properties: {
        goal_id: {
          type: "string",
          description: "The exact goal_id shown in the current active /goal prompt.",
        },
        reason: {
          type: "string",
          description: "The specific user or external action required to unblock the goal.",
        },
        evidence: {
          type: "string",
          description: "Concrete evidence from the repeated attempts that proves the impasse.",
        },
        repeated_turns: {
          type: "integer",
          minimum: 3,
          description: "Number of separate turns spent trying to resolve this same blocker.",
        },
      },
      required: ["goal_id", "reason", "evidence", "repeated_turns"],
    },
  },
  {
    name: TOOL_WAIT,
    description: `Keep the active /goal alive but quiet while an external event is expected. Call goal_wait alone after arranging a wake message, or provide resume_after_ms as a safety deadline. Requests below ${MIN_GOAL_WAIT_DELAY_MS}ms are clamped to ${MIN_GOAL_WAIT_DELAY_MS}ms. Do not use it for ordinary unfinished work.`,
    snippet:
      "Wait quietly for an external event without stopping the active /goal or starting automatic continuations",
    parameters: {
      type: "object",
      properties: {
        goal_id: {
          type: "string",
          description: "The exact goal_id shown in the current active /goal prompt.",
        },
        reason: {
          type: "string",
          description: "Why the goal is waiting and which external event should wake it.",
        },
        resume_after_ms: {
          type: "integer",
          minimum: 1,
          maximum: MAX_GOAL_WAIT_DELAY_MS,
          description: `Optional safety deadline in milliseconds that requests one continuation if no wake message arrives. Values below ${MIN_GOAL_WAIT_DELAY_MS} are accepted but clamped to ${MIN_GOAL_WAIT_DELAY_MS}.`,
        },
      },
      required: ["goal_id", "reason"],
    },
  },
];

export { MAX_GOAL_WAIT_DELAY_MS, MAX_GOAL_WAIT_REASON_LENGTH, MIN_GOAL_WAIT_DELAY_MS };
