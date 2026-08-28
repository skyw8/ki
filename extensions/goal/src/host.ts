import type { StdioRpc } from "./rpc.js";

export type Content = { type: string; text?: string };

export type EnqueueRequest = {
  content: Content[];
  deliverAs?: "queue" | "steer" | "nextTurn";
  when?: "now" | "settled";
  idempotencyKey?: string;
  kind?: "user" | "custom";
};

export type SessionSnapshot = {
  idle?: boolean;
  running?: boolean;
  queued?: number;
  extQueued?: number;
};

export type UIText = string | {
  key: string;
  params?: Record<string, string | number>;
  fallback?: string;
};

export type UIPanel = {
  title?: UIText;
  summary?: UIText;
  sections?: Array<Record<string, unknown>>;
  actions?: Array<{ id: string; label: UIText; style?: string; disabled?: boolean; title?: UIText }>;
  fields?: Array<{ id: string; label?: UIText; type?: string; value?: unknown; options?: string[] }>;
  submitLabel?: UIText;
};

export class Host {
  constructor(private readonly rpc: StdioRpc, private readonly sessionId: string) {}

  private params(value: unknown): Record<string, unknown> {
    const body = value && typeof value === "object" && !Array.isArray(value)
      ? { ...(value as Record<string, unknown>) }
      : {};
    body.sessionId = this.sessionId;
    return body;
  }

  enqueue(req: EnqueueRequest) {
    return this.rpc.call("session.enqueue", this.params(req));
  }

  snapshot(): Promise<SessionSnapshot> {
    return this.rpc.call("session.snapshot", this.params({})) as Promise<SessionSnapshot>;
  }

  appendEntry(customType: string, data: unknown) {
    return this.rpc.call("session.appendEntry", this.params({ customType, data }));
  }

  setStatus(key: string, text: UIText, tone: string) {
    return this.rpc.call("ui.setStatus", this.params({ key, text, tone }));
  }

  setPanel(panel: UIPanel) {
    return this.rpc.call("ui.setPanel", this.params(panel));
  }

  clearPanel() {
    return this.rpc.call("ui.clearPanel", this.params({}));
  }

  setGlobalStatus(key: string, text: UIText, tone: string) {
    return this.rpc.call("ui.setGlobalStatus", { key, text, tone });
  }

  setGlobalPanel(panel: UIPanel) {
    return this.rpc.call("ui.setGlobalPanel", panel);
  }

  clearGlobalPanel() {
    return this.rpc.call("ui.clearGlobalPanel", {});
  }

  confirm(title: UIText, message: UIText): Promise<boolean> {
    return this.rpc.call("ui.confirm", this.params({ title, message })).then((res) => {
      const row = res as { ok?: boolean } | null;
      return Boolean(row && row.ok);
    });
  }
}
