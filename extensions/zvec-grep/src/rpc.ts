import { createInterface } from "node:readline";
import { stdin, stdout } from "node:process";

export type RpcId = string | number;

export type RpcMsg = {
  jsonrpc?: string;
  id?: RpcId;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code?: number; message?: string };
};

type Pending = {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
};

export type RpcHandler = (method: string, params: unknown, id?: RpcId) => Promise<unknown> | unknown;

/**
 * Newline-delimited JSON-RPC transport shared by ki sidecars. Host calls carry
 * an id; host notifications (`cancel`, `config.updated`) do not, and this
 * sidecar's own outbound calls use a non-numeric id so the Host never confuses
 * them with the id of the request being served.
 */
export class StdioRpc {
  private readonly pending = new Map<string, Pending>();
  private readonly answered = new Set<string>();
  private seq = 1;
  private writeChain: Promise<void> = Promise.resolve();
  private handler: RpcHandler = async () => {
    throw new Error("no handler");
  };

  onRequest(handler: RpcHandler) {
    this.handler = handler;
  }

  start() {
    const rl = createInterface({ input: stdin, crlfDelay: Infinity });
    rl.on("line", (line) => {
      const trimmed = line.trim();
      if (!trimmed) return;
      let msg: RpcMsg;
      try {
        msg = JSON.parse(trimmed) as RpcMsg;
      } catch {
        return;
      }
      // Handlers run outside the readline callback so a slow host call cannot
      // block reading the next message; ordering per call is the Host's job.
      void this.dispatch(msg);
    });
  }

  call(method: string, params: unknown): Promise<unknown> {
    const id = `z${this.seq++}`;
    const p = new Promise<unknown>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
    });
    this.send({ jsonrpc: "2.0", id, method, params });
    return p;
  }

  notify(method: string, params: unknown = {}) {
    this.send({ jsonrpc: "2.0", method, params });
  }

  reply(id: RpcId, result: unknown) {
    this.send({ jsonrpc: "2.0", id, result: result ?? {} });
  }

  /**
   * replyOnce answers a request outside its handler, which `cancel` needs: the
   * handler for an abandoned tool call is still running, and its eventual reply
   * is then suppressed so the Host never sees two results for one id.
   */
  replyOnce(id: RpcId, result: unknown) {
    const key = String(id);
    if (this.answered.has(key)) return;
    this.answered.add(key);
    this.reply(id, result);
  }

  replyError(id: RpcId, message: string) {
    this.send({ jsonrpc: "2.0", id, error: { code: -32000, message } });
  }

  private async dispatch(msg: RpcMsg) {
    if (msg.method) {
      if (msg.id === undefined || msg.id === null) {
        try {
          await this.handler(msg.method, msg.params ?? {});
        } catch (err) {
          process.stderr.write(`zvec-grep notification ${msg.method}: ${safeError(err)}\n`);
        }
        return;
      }
      try {
        const result = await this.handler(msg.method, msg.params ?? {}, msg.id);
        const key = String(msg.id);
        // A cancel answered this id already; dropping the late result keeps the
        // reply count at one per request.
        if (this.answered.delete(key)) return;
        this.reply(msg.id, result);
      } catch (err) {
        this.replyError(msg.id, safeError(err));
      }
      return;
    }
    if (msg.id === undefined || msg.id === null) return;
    const pending = this.pending.get(String(msg.id));
    if (!pending) return;
    this.pending.delete(String(msg.id));
    if (msg.error) {
      pending.reject(new Error(msg.error.message || "rpc error"));
      return;
    }
    pending.resolve(msg.result);
  }

  private send(obj: unknown) {
    const line = `${JSON.stringify(obj)}\n`;
    this.writeChain = this.writeChain.then(
      () =>
        new Promise<void>((resolve, reject) => {
          stdout.write(line, (err) => (err ? reject(err) : resolve()));
        }),
    );
  }
}

export function safeError(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
