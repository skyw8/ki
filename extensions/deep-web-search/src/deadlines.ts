// Latency budgets in milliseconds.
//
// The host enforces a hard timeout on every tool call (see TOOL_SPECS.timeoutMs
// and internal/extension/rpc.go timeoutTool). That host value is a crash guard,
// not a control knob: a sidecar that relies on it to cut off a stalled provider
// pays the full host deadline and loses every successful partial result. These
// budgets keep the sidecar well inside the host deadline so a slow provider
// degrades to a bounded wait plus a `timeout` diagnostic.
//
// Codex is a long-budget provider: it runs a reasoning model with the hosted
// web_search tool, so it needs tens of seconds, unlike the plain-HTTP indexes.
export const TIMEOUTS = Object.freeze({
  codexSearch: 60_000,
  providerSearch: 15_000,
  providerContent: 20_000,
  providerGrace: 2_000,
  query: 65_000,
  tool: 110_000,
});

// Providers that need the long budget. They are never cut by the fast-provider
// grace window: a query that includes one waits for it up to its own budget.
export const LONG_PROVIDERS = Object.freeze(["codex"]);

export function providerSearchBudget(provider: string): number {
  return LONG_PROVIDERS.includes(provider) ? TIMEOUTS.codexSearch : TIMEOUTS.providerSearch;
}

// timeoutSignal bounds one HTTP request by `ms`, while still honouring an
// upstream (host cancel or aggregate query) abort.
export function timeoutSignal(parent: AbortSignal | undefined, ms: number = TIMEOUTS.providerSearch): AbortSignal {
  const timeout = AbortSignal.timeout(ms);
  return parent ? AbortSignal.any([parent, timeout]) : timeout;
}

// settleWithGrace resolves once every task settles, or `graceMs` after `quorum`
// tasks fulfilled — whichever comes first.
//
// Promise.allSettled alone cannot return stragglers' absence early, so a hung
// provider would hold the whole query until the host deadline. Entries left
// `undefined` mean the task was cut as a straggler; the caller turns those into
// `timeout` diagnostics and calls onGrace to abort the in-flight requests.
export async function settleWithGrace<T>(
  tasks: Promise<T>[],
  { quorum, graceMs, onGrace }: { quorum: number; graceMs: number; onGrace?: () => void },
): Promise<(PromiseSettledResult<T> | undefined)[]> {
  if (!tasks.length) return [];
  const results: (PromiseSettledResult<T> | undefined)[] = new Array(tasks.length);
  let remaining = tasks.length;
  let fulfilled = 0;
  let grace: ReturnType<typeof setTimeout> | undefined;
  let done = false;
  return await new Promise((resolve) => {
    const finish = () => {
      if (done) return;
      done = true;
      if (grace !== undefined) clearTimeout(grace);
      resolve(results);
    };
    tasks.forEach((task, index) => {
      task.then(
        (value) => { results[index] = { status: "fulfilled", value }; fulfilled++; },
        (reason) => { results[index] = { status: "rejected", reason }; },
      ).finally(() => {
        if (done) return;
        remaining--;
        if (remaining === 0) {
          finish();
          return;
        }
        if (fulfilled >= quorum && grace === undefined) {
          grace = setTimeout(() => { onGrace?.(); finish(); }, graceMs);
        }
      });
    });
  });
}
