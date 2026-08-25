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

export class StdioRpc {
  private readonly pending = new Map<string, Pending>();
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
      void this.dispatch(msg);
    });
  }

  call(method: string, params: unknown): Promise<unknown> {
    // Host outbound ids are "1","2",… — a numeric id here would be delivered
    // as the initialize/command result and drop subscriptions.
    const id = `g${this.seq++}`;
    const key = id;
    const p = new Promise<unknown>((resolve, reject) => {
      this.pending.set(key, { resolve, reject });
    });
    this.send({ jsonrpc: "2.0", id, method, params });
    return p;
  }

  reply(id: RpcId, result: unknown) {
    this.send({ jsonrpc: "2.0", id, result });
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
          process.stderr.write(`goal notification ${msg.method}: ${String(err)}\n`);
        }
        return;
      }
      try {
        const result = await this.handler(msg.method, msg.params ?? {}, msg.id);
        this.reply(msg.id, result ?? {});
      } catch (err) {
        this.replyError(msg.id, err instanceof Error ? err.message : String(err));
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
