import type { StdioRpc } from "./rpc.js";

export type UIText = string | { key: string; params?: Record<string, string | number>; fallback?: string };

export type SessionSnapshot = {
  idle?: boolean;
  running?: boolean;
  queued?: number;
  extQueued?: number;
};

/**
 * Host is the session-scoped part of the ki extension API this sidecar uses.
 * Everything is addressed by sessionId so one sidecar process can serve many
 * sessions.
 */
export class Host {
  constructor(
    private readonly rpc: StdioRpc,
    readonly sessionId: string,
  ) {}

  private params(value: unknown): Record<string, unknown> {
    const body = value && typeof value === "object" && !Array.isArray(value) ? { ...(value as Record<string, unknown>) } : {};
    body.sessionId = this.sessionId;
    return body;
  }

  setStatus(key: string, text: UIText, tone = "info"): Promise<unknown> {
    return this.rpc.call("ui.setStatus", this.params({ key, text, tone }));
  }

  clearStatus(key: string): Promise<unknown> {
    return this.setStatus(key, "");
  }

  appendEntry(customType: string, data: unknown): Promise<unknown> {
    return this.rpc.call("session.appendEntry", this.params({ customType, data }));
  }

  enqueue(text: string, options: { when?: "now" | "settled"; idempotencyKey?: string; kind?: string } = {}): Promise<unknown> {
    return this.rpc.call("session.enqueue", this.params({
      content: [{ type: "text", text }],
      deliverAs: "queue",
      when: options.when ?? "settled",
      idempotencyKey: options.idempotencyKey,
      kind: options.kind ?? "custom",
      display: true,
    }));
  }

  snapshot(): Promise<SessionSnapshot> {
    return this.rpc.call("session.snapshot", this.params({})) as Promise<SessionSnapshot>;
  }

  confirm(title: UIText, message: UIText): Promise<boolean> {
    return this.rpc.call("ui.confirm", this.params({ title, message })).then((res) => {
      const row = res as { ok?: boolean } | null;
      return Boolean(row && row.ok);
    });
  }
}
