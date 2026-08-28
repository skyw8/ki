import { createInterface } from "node:readline";
import { stdin, stdout } from "node:process";

/** Minimal newline-delimited JSON-RPC transport used by Ki sidecars. */
export class StdioRpc {
  handler: (method: string, params: any, id?: string | number) => Promise<any>;
  pending: Map<string, { resolve: (value: any) => void; reject: (reason?: any) => void }>;
  sequence: number;
  writeChain: Promise<void>;

  constructor() {
    this.handler = async () => ({});
    this.pending = new Map();
    this.sequence = 1;
    this.writeChain = Promise.resolve();
  }

  onRequest(handler: (method: string, params: any, id?: string | number) => Promise<any>) {
    this.handler = handler;
  }

  start() {
    const lines = createInterface({ input: stdin, crlfDelay: Infinity });
    lines.on("line", (line) => {
      const text = line.trim();
      if (!text) return;
      let message;
      try {
        message = JSON.parse(text);
      } catch {
        return;
      }
      void this.dispatch(message);
    });
  }

  notify(method, params = {}) {
    this.send({ jsonrpc: "2.0", method, params });
  }

  call(method: string, params: any = {}): Promise<any> {
    const id = `g${this.sequence++}`;
    const promise = new Promise((resolve, reject) => this.pending.set(String(id), { resolve, reject }));
    this.send({ jsonrpc: "2.0", id, method, params });
    return promise;
  }

  async dispatch(message) {
    if (typeof message.method === "string") {
      if (message.id === undefined || message.id === null) {
        try {
          await this.handler(message.method, message.params ?? {}, undefined);
        } catch (error) {
          process.stderr.write(`deep-web-search notification ${message.method}: ${safeError(error)}\n`);
        }
        return;
      }
      try {
        const result = await this.handler(message.method, message.params ?? {}, message.id);
        this.send({ jsonrpc: "2.0", id: message.id, result: result ?? {} });
      } catch (error) {
        this.send({ jsonrpc: "2.0", id: message.id, error: { code: -32000, message: safeError(error) } });
      }
      return;
    }
    if (message.id === undefined || message.id === null) return;
    const pending = this.pending.get(String(message.id));
    if (!pending) return;
    this.pending.delete(String(message.id));
    if (message.error) {
      pending.reject(new Error(message.error.message || "host rpc error"));
    } else {
      pending.resolve(message.result);
    }
  }

  send(message: any) {
    this.writeChain = this.writeChain.then(() => new Promise<void>((resolve, reject) => {
      stdout.write(`${JSON.stringify(message)}\n`, (error) => error ? reject(error) : resolve());
    })).catch((error) => { process.stderr.write(`deep-web-search rpc write: ${safeError(error)}\n`); });
  }
}

export function safeError(error) {
  return error instanceof Error ? error.message : String(error);
}
