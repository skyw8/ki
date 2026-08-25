import { MAX_OBJECTIVE_LENGTH } from "./prompts.js";

export type CommandKind = "show" | "start" | "pause" | "resume" | "clear" | "edit";

export type ParsedCommand =
  | { kind: CommandKind; objective?: string }
  | { error: string };

export const COMPLETIONS = ["pause", "resume", "clear", "edit", "status"];

export function parseCommand(args: string): ParsedCommand {
  const tokens = tokenize(args.trim());
  if (tokens.length === 0) return { kind: "show" };
  const [first, ...rest] = tokens;
  if (first === "pause") return rest.length === 0 ? { kind: "pause" } : { error: "Usage: /goal pause" };
  if (first === "resume") return rest.length === 0 ? { kind: "resume" } : { error: "Usage: /goal resume" };
  if (first === "clear" || first === "stop") {
    return rest.length === 0 ? { kind: "clear" } : { error: "Usage: /goal clear" };
  }
  if (first === "status") return rest.length === 0 ? { kind: "show" } : { error: "Usage: /goal status" };
  if (first === "edit") return parseObjective("edit", rest);
  return parseObjective("start", tokens);
}

function parseObjective(kind: "start" | "edit", tokens: string[]): ParsedCommand {
  const objective = tokens.join(" ").trim();
  if (!objective) {
    return { error: kind === "edit" ? "Usage: /goal edit <objective>" : "Usage: /goal <objective>" };
  }
  if (objective.length > MAX_OBJECTIVE_LENGTH) {
    return { error: `Objective is too long (max ${MAX_OBJECTIVE_LENGTH} characters).` };
  }
  return { kind, objective };
}

function tokenize(input: string): string[] {
  if (!input) return [];
  return input.split(/\s+/).filter(Boolean);
}
