import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { Host, UIText } from "./host.js";
import { parseCommand } from "./command.js";
import {
  appendSystem,
  buildContinuePrompt,
  buildGoalPrompt,
  buildGoalSystemPrompt,
  buildObjectiveUpdatedPrompt,
  buildResumePrompt,
  buildWaitingResumePrompt,
  MAX_AUTOMATIC_TURNS,
  type GoalPromptContext,
  type GoalStatus,
} from "./prompts.js";
import {
  isContradictoryCompletionSummary,
  MAX_BLOCKER_EVIDENCE_LENGTH,
  MAX_BLOCKER_REASON_LENGTH,
  MAX_COMPLETION_SUMMARY_LENGTH,
  MAX_GOAL_ID_LENGTH,
  MAX_GOAL_WAIT_DELAY_MS,
  MAX_GOAL_WAIT_REASON_LENGTH,
  TOOL_BLOCKED,
  TOOL_COMPLETE,
  TOOL_WAIT,
} from "./tools.js";
import {
  createGoalWait,
  type GoalWait,
  GoalWaitTimer,
  normalizeGoalWait,
  resolveGoalWaitDelay,
} from "./wait.js";

export type { GoalStatus };

export type GoalState = {
  id: string;
  text: string;
  status: GoalStatus;
  startedAt: number;
  updatedAt: number;
  iteration: number;
  automaticTurns: number;
  waiting?: GoalWait;
};

type StoreFile = { goal: GoalState | null };

const CUSTOM_TYPE = "goal-state";
const STATUS_KEY = "goal";

function uiText(key: string): UIText {
  return { key };
}

export class GoalApp {
  private goal: GoalState | null = null;
  private chain: Promise<unknown> = Promise.resolve();
  private readonly waitTimer = new GoalWaitTimer();

  constructor(
    private readonly host: Host,
    private readonly home: string,
    private readonly sessionId: string,
  ) {}

  close() {
    this.waitTimer.clear();
  }

  private exclusive<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.chain.then(fn, fn);
    this.chain = run.then(
      () => undefined,
      () => undefined,
    );
    return run;
  }

  async restore(): Promise<boolean> {
    this.goal = await loadGoal(this.statePath());
    await this.syncUI();
    this.restoreWaitTimer();
    return this.goal?.status === "active" && !this.goal.waiting;
  }

  // Host applies initialize tools only after this RPC returns. Delay so a
  // restored active goal does not occupy before the sidecar is in the chain.
  scheduleRestoreContinue() {
    setTimeout(() => {
      void this.exclusive(async () => {
        if (!this.goal || this.goal.status !== "active" || this.goal.waiting) return;
        try {
          const snap = await this.host.snapshot();
          if (snap.running || snap.idle === false) return;
          await this.enqueueContinue();
        } catch (err) {
          process.stderr.write(`goal restore continue: ${String(err)}\n`);
        }
      });
    }, 100);
  }

  async onBeforeAgentStart(payload: { system?: string }): Promise<{ system?: string }> {
    const goal = this.goal;
    if (!goal || goal.status !== "active") return {};
    const system = typeof payload.system === "string" ? payload.system : "";
    return { system: appendSystem(system, buildGoalSystemPrompt(this.promptCtx(goal))) };
  }

  onAgentSettled(): Promise<void> {
    return this.exclusive(async () => {
      if (!this.goal || this.goal.status !== "active" || this.goal.waiting) return;
      await this.enqueueContinue();
    });
  }

  invokeCommand(args: string): Promise<{ handled: boolean; notice?: string; prompt?: string }> {
    return this.exclusive(async () => {
      const parsed = parseCommand(args);
      if ("error" in parsed) return { handled: true, notice: parsed.error };
      switch (parsed.kind) {
        case "show":
          return { handled: true, notice: this.statusNotice() };
        case "pause":
          return this.pause("Paused.");
        case "resume":
          return this.resume();
        case "clear":
          return this.clear();
        case "edit":
          return this.edit(parsed.objective ?? "");
        case "start":
          return this.start(parsed.objective ?? "");
        default:
          return { handled: true, notice: this.statusNotice() };
      }
    });
  }

  onAction(id: string): Promise<void> {
    return this.exclusive(async () => {
      if (id === "pause") {
        await this.pause("Paused.");
        return;
      }
      if (id === "resume") {
        const out = await this.resume();
        if (out.prompt) await this.enqueueUser(out.prompt, `goal-resume:${this.goal?.id ?? ""}`);
        return;
      }
      if (id === "clear") {
        const ok = await this.host.confirm({ key: "confirmClear" }, this.goal ? this.goal.text : { key: "noActiveGoal" });
        if (!ok) return;
        await this.clear();
      }
    });
  }

  onSubmit(fields: Record<string, unknown>): Promise<void> {
    return this.exclusive(async () => {
      const objective = str(fields.objective);
      if (!objective) return;
      const out = !this.goal || this.goal.status === "complete"
        ? await this.start(objective)
        : await this.edit(objective);
      if (out.prompt) await this.enqueueUser(out.prompt, `goal-form:${this.goal?.id ?? ""}`);
    });
  }

  executeTool(
    name: string,
    args: Record<string, unknown>,
  ): Promise<{ content: Array<{ type: string; text: string }>; isError?: boolean }> {
    return this.exclusive(async () => {
      if (name === TOOL_COMPLETE) return this.complete(args);
      if (name === TOOL_BLOCKED) return this.block(args);
      if (name === TOOL_WAIT) return this.wait(args);
      return textResult(`unknown tool ${name}`, true);
    });
  }

  private promptCtx(goal: GoalState): GoalPromptContext {
    return {
      id: goal.id,
      text: goal.text,
      status: goal.status,
      iteration: goal.iteration,
      tokensUsed: 0,
      startedAt: goal.startedAt,
      updatedAt: goal.updatedAt,
      timeUsedSeconds: 0,
      baselineTokens: 0,
    };
  }

  private async start(objective: string) {
    if (this.goal && this.goal.status !== "complete") {
      return {
        handled: true,
        notice: `Goal already ${this.goal.status}. /goal clear or /goal edit <objective>.`,
      };
    }
    this.waitTimer.clear();
    this.goal = newGoal(objective);
    await this.persist();
    return { handled: false, prompt: buildGoalPrompt(this.promptCtx(this.goal)) };
  }

  private async edit(objective: string) {
    if (!this.goal || this.goal.status === "complete") {
      return { handled: true, notice: "No active goal to edit. /goal <objective> to start one." };
    }
    this.waitTimer.clear();
    this.goal = {
      ...this.goal,
      id: randomUUID(),
      text: objective,
      status: "active",
      updatedAt: Date.now(),
      iteration: 0,
      automaticTurns: 0,
      waiting: undefined,
    };
    await this.persist();
    return { handled: false, prompt: buildObjectiveUpdatedPrompt(this.promptCtx(this.goal)) };
  }

  private async pause(notice: string) {
    if (!this.goal || this.goal.status !== "active") {
      return { handled: true, notice: "No active goal to pause." };
    }
    this.waitTimer.clear();
    this.goal = { ...this.goal, status: "paused", updatedAt: Date.now(), waiting: undefined };
    await this.persist();
    return { handled: true, notice };
  }

  private async resume() {
    if (!this.goal) return { handled: true, notice: "No paused or blocked goal to resume." };
    const waitingReason = this.goal.waiting?.reason;
    const from = this.goal.status;
    if (from !== "paused" && from !== "blocked" && !waitingReason) {
      return { handled: true, notice: "No paused, blocked, or waiting goal to resume." };
    }
    this.waitTimer.clear();
    this.goal = {
      ...this.goal,
      status: "active",
      updatedAt: Date.now(),
      automaticTurns: 0,
      waiting: undefined,
    };
    await this.persist();
    const prompt = waitingReason
      ? buildWaitingResumePrompt(this.promptCtx(this.goal), waitingReason)
      : buildResumePrompt(this.promptCtx(this.goal), from);
    return { handled: false, prompt };
  }

  private async clear() {
    if (!this.goal) return { handled: true, notice: "No goal to clear." };
    this.waitTimer.clear();
    this.goal = null;
    await this.persist();
    return { handled: true, notice: "Goal cleared." };
  }

  private async complete(args: Record<string, unknown>) {
    const goalId = str(args.goal_id);
    const summary = str(args.summary);
    const stale = this.staleReason(goalId);
    if (stale) return textResult(`Goal completion rejected: ${stale}.`, true);
    if (!summary) return textResult("Goal completion rejected: summary is required.", true);
    if (summary.length > MAX_COMPLETION_SUMMARY_LENGTH) {
      return textResult("Goal completion rejected: summary is too long.", true);
    }
    if (isContradictoryCompletionSummary(summary)) {
      return textResult("Goal completion rejected: summary says the goal is not complete.", true);
    }
    this.waitTimer.clear();
    this.goal = { ...this.goal!, status: "complete", updatedAt: Date.now(), waiting: undefined };
    await this.persist();
    return textResult(`Goal complete: ${summary}`);
  }

  private async block(args: Record<string, unknown>) {
    const goalId = str(args.goal_id);
    const reason = str(args.reason);
    const evidence = str(args.evidence);
    const repeatedTurns = num(args.repeated_turns);
    const stale = this.staleReason(goalId);
    if (stale) return textResult(`goal_blocked rejected: ${stale}.`, true);
    if (!reason) return textResult("goal_blocked rejected: reason is empty.", true);
    if (reason.length > MAX_BLOCKER_REASON_LENGTH) {
      return textResult("goal_blocked rejected: reason is too long.", true);
    }
    if (!evidence) return textResult("goal_blocked rejected: evidence is empty.", true);
    if (evidence.length > MAX_BLOCKER_EVIDENCE_LENGTH) {
      return textResult("goal_blocked rejected: evidence is too long.", true);
    }
    if (!Number.isInteger(repeatedTurns)) {
      return textResult("goal_blocked rejected: repeated_turns must be a whole number.", true);
    }
    if (repeatedTurns < 3) {
      return textResult("goal_blocked rejected: repeated_turns must be at least 3.", true);
    }
    this.waitTimer.clear();
    this.goal = { ...this.goal!, status: "blocked", updatedAt: Date.now(), waiting: undefined };
    await this.persist();
    return textResult(`Goal blocked: ${reason}`);
  }

  private async wait(args: Record<string, unknown>) {
    const goalId = str(args.goal_id);
    const reason = str(args.reason);
    const resumeAfterMs = args.resume_after_ms === undefined ? undefined : num(args.resume_after_ms);
    const stale = this.staleReason(goalId);
    if (stale) return textResult(`goal_wait rejected: ${stale}.`, true);
    if (this.goal?.waiting) return textResult("goal_wait rejected: goal is already waiting.", true);
    if (!reason) return textResult("goal_wait rejected: reason is empty.", true);
    if (reason.length > MAX_GOAL_WAIT_REASON_LENGTH) {
      return textResult("goal_wait rejected: reason is too long.", true);
    }
    if (
      resumeAfterMs !== undefined &&
      (!Number.isInteger(resumeAfterMs) || resumeAfterMs < 1 || resumeAfterMs > MAX_GOAL_WAIT_DELAY_MS)
    ) {
      return textResult(
        `goal_wait rejected: resume_after_ms must be a whole number from 1 to ${MAX_GOAL_WAIT_DELAY_MS}.`,
        true,
      );
    }
    const { requestedMs, effectiveMs } = resolveGoalWaitDelay(resumeAfterMs);
    const waiting = createGoalWait(reason, resumeAfterMs);
    this.goal = { ...this.goal!, updatedAt: Date.now(), waiting };
    await this.persist();
    this.restoreWaitTimer();
    const clamped = requestedMs !== undefined && effectiveMs !== requestedMs;
    const text = clamped
      ? `Goal waiting: ${reason}\nRequested resume_after_ms ${requestedMs} was clamped to ${effectiveMs}.`
      : `Goal waiting: ${reason}`;
    return textResult(text);
  }

  private staleReason(goalId: string): string | undefined {
    if (!this.goal) return "no active goal";
    if (this.goal.status !== "active") return `goal is ${this.goal.status}, not active`;
    if (!goalId) return "missing goal_id";
    if (goalId.length > MAX_GOAL_ID_LENGTH) return "goal_id is too long";
    if (goalId !== this.goal.id) return "goal_id does not match the active goal";
    return undefined;
  }

  private async enqueueContinue() {
    const goal = this.goal;
    if (!goal || goal.status !== "active" || goal.waiting) return;
    if (goal.automaticTurns >= MAX_AUTOMATIC_TURNS) {
      this.goal = { ...goal, status: "paused", updatedAt: Date.now() };
      await this.persist();
      return;
    }
    const next = {
      ...goal,
      automaticTurns: goal.automaticTurns + 1,
      iteration: goal.iteration + 1,
      updatedAt: Date.now(),
    };
    this.goal = next;
    await this.persist();
    const marker = `${next.id}:${next.iteration}`;
    await this.enqueueUser(
      buildContinuePrompt(this.promptCtx(next), marker),
      `goal-continue:${marker}`,
      "settled",
    );
  }

  private async wakeFromWait() {
    const goal = this.goal;
    if (!goal || goal.status !== "active" || !goal.waiting) return;
    const reason = goal.waiting.reason;
    this.goal = { ...goal, waiting: undefined, updatedAt: Date.now() };
    await this.persist();
    await this.enqueueUser(
      buildWaitingResumePrompt(this.promptCtx(this.goal), reason),
      `goal-wait-resume:${goal.id}:${goal.updatedAt}`,
      "settled",
    );
  }

  private restoreWaitTimer() {
    this.waitTimer.clear();
    const resumeAt = this.goal?.waiting?.resumeAt;
    if (!this.goal || this.goal.status !== "active" || resumeAt === undefined) return;
    this.waitTimer.schedule(resumeAt, () => {
      void this.exclusive(() => this.wakeFromWait());
    });
  }

  private async enqueueUser(text: string, key: string, when: "now" | "settled" = "now") {
    await this.host.enqueue({
      content: [{ type: "text", text }],
      deliverAs: "queue",
      when,
      idempotencyKey: key,
      kind: "user",
    });
  }

  private statusNotice(): string {
    if (!this.goal) return "No /goal in this session. /goal <objective> to start.";
    const wait = this.goal.waiting ? `\nwaiting: ${this.goal.waiting.reason}` : "";
    return `Goal · ${this.goal.status}${wait}\n${this.goal.text}`;
  }

  private async persist() {
    await saveGoal(this.statePath(), this.goal);
    try {
      await this.host.appendEntry(CUSTOM_TYPE, { goal: this.goal });
    } catch (err) {
      process.stderr.write(`goal appendEntry: ${String(err)}\n`);
    }
    await this.syncUI();
  }

  private async syncUI() {
    try {
      const goal = this.goal;
      if (!goal) {
        await this.host.setStatus(STATUS_KEY, uiText("title"), "info");
        await this.host.setPanel({
          title: uiText("title"),
          summary: uiText("noGoal"),
          fields: [{ id: "objective", label: uiText("objective"), type: "textarea", value: "" }],
          submitLabel: uiText("start"),
          actions: [
            { id: "pause", label: uiText("pause"), disabled: true, title: uiText("noActive") },
            { id: "resume", label: uiText("resume"), disabled: true, title: uiText("noPaused") },
            { id: "clear", label: uiText("clear"), style: "danger", disabled: true, title: uiText("noGoalToClear") },
          ],
        });
        return;
      }
      const waiting = Boolean(goal.waiting);
      const label = waiting ? "waiting" : goal.status;
      const tone =
        goal.status === "complete" ? "success" : waiting || goal.status !== "active" ? "warning" : "active";
      await this.host.setStatus(STATUS_KEY, uiText(`statusLine.${label}`), tone);
      const canPause = goal.status === "active" && !waiting;
      const canResume = goal.status === "paused" || goal.status === "blocked" || waiting;
      const canEdit = goal.status !== "complete";
      const items: Array<{ label: UIText; value: UIText }> = [
        { label: uiText("status"), value: uiText(`status.${label}`) },
        { label: uiText("turns"), value: `${goal.automaticTurns} / ${MAX_AUTOMATIC_TURNS}` },
        { label: uiText("started"), value: formatTime(goal.startedAt) },
        { label: uiText("updated"), value: formatTime(goal.updatedAt) },
        { label: uiText("id"), value: goal.id },
      ];
      if (goal.waiting) {
        items.splice(1, 0, { label: uiText("waiting"), value: goal.waiting.reason });
        if (goal.waiting.resumeAt) items.splice(2, 0, { label: uiText("resumeAt"), value: formatTime(goal.waiting.resumeAt) });
      }
      await this.host.setPanel({
        title: uiText("title"),
        sections: [{ heading: uiText("details"), items }],
        fields: [{ id: "objective", label: uiText("objective"), type: "textarea", value: goal.text }],
        submitLabel: canEdit ? uiText("update") : uiText("start"),
        actions: [
          { id: "pause", label: uiText("pause"), disabled: !canPause, title: canPause ? uiText("pauseHint") : uiText("onlyActivePause") },
          { id: "resume", label: uiText("resume"), disabled: !canResume, title: canResume ? uiText("resumeHint") : uiText("nothingToResume") },
          { id: "clear", label: uiText("clear"), style: "danger", disabled: false, title: uiText("clearHint") },
        ],
      });
    } catch (err) {
      process.stderr.write(`goal ui: ${String(err)}\n`);
    }
  }

  private statePath(): string {
    return join(this.home, "goal", `${this.sessionId}.json`);
  }
}

function newGoal(text: string): GoalState {
  const now = Date.now();
  return {
    id: randomUUID(),
    text,
    status: "active",
    startedAt: now,
    updatedAt: now,
    iteration: 0,
    automaticTurns: 0,
  };
}

async function loadGoal(path: string): Promise<GoalState | null> {
  try {
    const raw = await readFile(path, "utf8");
    const parsed = JSON.parse(raw) as StoreFile;
    if (!parsed || !parsed.goal || typeof parsed.goal.id !== "string") return null;
    const goal = parsed.goal;
    return {
      ...goal,
      iteration: typeof goal.iteration === "number" ? goal.iteration : 0,
      automaticTurns: typeof goal.automaticTurns === "number" ? goal.automaticTurns : 0,
      waiting: normalizeGoalWait(goal.waiting),
    };
  } catch {
    return null;
  }
}

async function saveGoal(path: string, goal: GoalState | null) {
  await mkdir(dirname(path), { recursive: true });
  const tmp = `${path}.${process.pid}.tmp`;
  await writeFile(tmp, `${JSON.stringify({ goal }, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  await rename(tmp, path);
}

function formatTime(ms: number): string {
  if (!ms) return "—";
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(ms);
  }
}

function str(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function num(value: unknown): number {
  return typeof value === "number" ? value : Number.NaN;
}

function textResult(text: string, isError = false) {
  return { content: [{ type: "text", text }], isError };
}
