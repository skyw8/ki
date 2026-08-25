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

export type UIPanel = {
  title?: string;
  summary?: string;
  sections?: Array<Record<string, unknown>>;
  actions?: Array<{ id: string; label: string; style?: string; disabled?: boolean; title?: string }>;
  fields?: Array<{ id: string; label?: string; type?: string; value?: unknown; options?: string[] }>;
  submitLabel?: string;
};

export class Host {
  constructor(private readonly rpc: StdioRpc) {}

  enqueue(req: EnqueueRequest) {
    return this.rpc.call("session.enqueue", req);
  }

  snapshot(): Promise<SessionSnapshot> {
    return this.rpc.call("session.snapshot", {}) as Promise<SessionSnapshot>;
  }

  appendEntry(customType: string, data: unknown) {
    return this.rpc.call("session.appendEntry", { customType, data });
  }

  setStatus(key: string, text: string, tone: string) {
    return this.rpc.call("ui.setStatus", { key, text, tone });
  }

  setPanel(panel: UIPanel) {
    return this.rpc.call("ui.setPanel", panel);
  }

  clearPanel() {
    return this.rpc.call("ui.clearPanel", {});
  }

  confirm(title: string, message: string): Promise<boolean> {
    return this.rpc.call("ui.confirm", { title, message }).then((res) => {
      const row = res as { ok?: boolean } | null;
      return Boolean(row && row.ok);
    });
  }
}
