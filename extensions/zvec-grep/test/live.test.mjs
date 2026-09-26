// Live test against the real @zvec/zvec-grep package and the real zg CLI.
// Opt in with KI_ZVEC_GREP_LIVE=1: the first run downloads the local embedding
// model and builds an index, so it is kept out of the default test run.
import test from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { startSidecar } from "./harness.mjs";

const enabled = process.env.KI_ZVEC_GREP_LIVE === "1";

test("indexes a workspace and answers a semantic query through the real library", { timeout: 600_000, skip: !enabled }, async (t) => {
  const sidecar = startSidecar(t, { fake: false });
  mkdirSync(join(sidecar.workspace, "src"), { recursive: true });
  writeFileSync(
    join(sidecar.workspace, "src", "theme.ts"),
    "export function useTheme() {\n  const [theme, setTheme] = useState(\"light\");\n  useEffect(() => saveTheme(theme), [theme]);\n  return { theme, setTheme };\n}\n",
  );
  writeFileSync(
    join(sidecar.workspace, "README.md"),
    "# Sample project\n\nTheme preference persistence on startup is handled by useTheme.\n",
  );

  await sidecar.call("session.open", { sessionId: "s1", cwd: sidecar.workspace });
  const started = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-index", args: "" });
  assert.equal(started.result.handled, true);
  assert.match(started.result.notice, /Started/);

  const done = await sidecar.waitForInbound(
    (entry) => entry.method === "ui.setStatus" && entry.params.text && entry.params.text.key === "index.done",
    "the index job to finish",
    // The first run may download the embedding model in addition to indexing.
    300_000,
  );
  assert.equal(done.params.tone, "success");

  const searched = await sidecar.call("tool.execute", {
    sessionId: "s1",
    name: "zvec_grep_search",
    args: { query: "where is the theme preference persisted on startup", limit: 3 },
  });
  assert.notEqual(searched.result.isError, true, JSON.stringify(searched.result));
  const text = searched.result.content[0].text;
  assert.match(text, /freshness: (fresh|possibly_stale)/);
  assert.match(text, /src\/theme\.ts/);
  assert.match(text, /1\t/);
  assert.ok(searched.result.details.items >= 1, JSON.stringify(searched.result.details));

  const status = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-status", args: "" });
  assert.match(status.result.notice, /ready|Coverage/i);

  const removed = await sidecar.call("command.invoke", { sessionId: "s1", name: "zg-remove", args: "" });
  assert.equal(removed.result.handled, true);
});
