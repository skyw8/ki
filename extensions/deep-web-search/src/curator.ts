import { randomUUID } from "node:crypto";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { URL } from "node:url";
import { compactText } from "./normalize.js";

function json(res: any, status: number, value: any) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  res.end(JSON.stringify(value));
}

function html(res: any, value: string) {
  res.statusCode = 200;
  res.setHeader("Content-Type", "text/html; charset=utf-8");
  res.end(value);
}

function readBody(req: any): Promise<any> {
  return new Promise<any>((resolve, reject) => {
    let body = "";
    req.on("data", (chunk) => { body += chunk; if (body.length > 1_000_000) req.destroy(new Error("request too large")); });
    req.on("end", () => { try { resolve(body ? JSON.parse(body) : {}); } catch (error) { reject(error); } });
    req.on("error", reject);
  });
}

function openBrowser(url) {
  const command = process.platform === "darwin" ? "open" : process.platform === "win32" ? "cmd" : "xdg-open";
  const args = process.platform === "win32" ? ["/c", "start", "", url] : [url];
  try { spawn(command, args, { detached: true, stdio: "ignore" }).unref(); } catch { /* the URL is still returned to the caller */ }
}

function page(token) {
  const safeToken = JSON.stringify(token);
  return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Deep Web Search Review</title><style>
body{font:15px system-ui,sans-serif;max-width:980px;margin:24px auto;padding:0 16px;color:#202124;background:#fafafa}h1{font-size:22px}textarea{width:100%;min-height:70px;box-sizing:border-box;padding:8px}button{padding:8px 12px;margin:4px;border:1px solid #bbb;border-radius:6px;background:#fff;cursor:pointer}button.primary{background:#175cd3;color:#fff;border-color:#175cd3}.source{padding:12px;margin:10px 0;background:#fff;border:1px solid #ddd;border-radius:8px}.source small{display:block;color:#666;overflow-wrap:anywhere}.summary{white-space:pre-wrap;background:#eef5ff;padding:12px;border-radius:8px}.muted{color:#666}.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
</style></head><body><h1>Deep Web Search · source review</h1><p class="muted">Select trusted sources, optionally rewrite or add a query, then submit the evidence pack.</p><div class="row"><input id="query" style="flex:1;min-width:260px" placeholder="additional query"><button onclick="rewrite()">Rewrite query</button><button onclick="addSearch()">Add search</button></div><div id="status" class="muted">Connecting…</div><h2>Summary draft</h2><div id="summary" class="summary">No draft yet.</div><div id="sources"></div><div class="row"><button class="primary" onclick="summarize()">Generate summary</button><button class="primary" onclick="submitReview()">Submit selected sources</button><button onclick="cancelReview()">Cancel</button></div><script>
const token=${safeToken};let state={};const status=document.getElementById('status');
function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function render(next){state=next;status.textContent=(next.status||'ready')+' · '+(next.queries||[]).join(' | ');document.getElementById('summary').textContent=next.summary||'No draft yet.';document.getElementById('sources').innerHTML=(next.results||[]).map((x,i)=>'<div class="source"><label><input type="checkbox" data-url="'+esc(x.url)+'" checked> '+esc(x.title)+'</label><small>'+esc(x.url)+'</small><p>'+esc(x.snippet||'')+'</p></div>').join('')}
function selected(){return [...document.querySelectorAll('[data-url]:checked')].map(x=>x.dataset.url)}
async function post(path,body){const r=await fetch(path+'?token='+encodeURIComponent(token),{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body||{})});const x=await r.json();if(!r.ok)throw Error(x.error||'request failed');return x}
async function rewrite(){try{const q=document.getElementById('query').value||state.queries?.[0]||'';const x=await post('/rewrite',{query:q});document.getElementById('query').value=x.query;status.textContent='Rewritten query ready.'}catch(e){status.textContent=e.message}}
async function addSearch(){try{const q=document.getElementById('query').value.trim();if(!q)return;await post('/add-search',{query:q});document.getElementById('query').value=''}catch(e){status.textContent=e.message}}
async function summarize(){try{const x=await post('/summarize',{selectedUrls:selected()});render(x)}catch(e){status.textContent=e.message}}
async function submitReview(){try{await post('/submit',{selectedUrls:selected()});status.textContent='Submitted; this window can be closed.'}catch(e){status.textContent=e.message}}
async function cancelReview(){try{await post('/cancel',{});status.textContent='Cancelled.'}catch(e){status.textContent=e.message}}
const events=new EventSource('/events?token='+encodeURIComponent(token));events.onmessage=e=>render(JSON.parse(e.data));events.onerror=()=>status.textContent='Connection lost; the search result is still available in Ki.';
</script></body></html>`;
}

export async function startCurator({ queries, result, config, onAddSearch, onRewrite, onSummarize }) {
  const token = randomUUID();
  const clients = new Set<any>();
  const publicResults = (items) => (items || []).map((item) => ({ title: item.title, url: item.url, snippet: item.snippet, providers: item.providers }));
  const state: any = { status: "ready", queries: [...queries], results: publicResults(result.results), summary: "", responseId: result.responseId };
  let finished = false;
  let timer;
  let resolveFinished: (value: any) => void = () => {};
  const finishedPromise = new Promise<any>((resolve) => { resolveFinished = resolve; });
  const broadcast = () => {
    const message = `data: ${JSON.stringify(state)}\n\n`;
    for (const client of clients) client.write(message);
  };
  const finish = (value) => {
    if (finished) return;
    finished = true;
    state.status = value.status || "submitted";
    broadcast();
    for (const client of clients) client.end();
    resolveFinished(value);
  };
  let server: any;
  try {
    server = createServer(async (req, res) => {
      try {
        const requestUrl = new URL(req.url || "/", "http://127.0.0.1");
        if (requestUrl.searchParams.get("token") !== token) return json(res, 403, { error: "invalid curator token" });
        if (req.method === "GET" && (requestUrl.pathname === "/" || requestUrl.pathname === "/curator")) return html(res, page(token));
        if (req.method === "GET" && requestUrl.pathname === "/events") {
          res.statusCode = 200; res.setHeader("Content-Type", "text/event-stream"); res.setHeader("Cache-Control", "no-cache"); res.setHeader("Connection", "keep-alive"); res.write(`data: ${JSON.stringify(state)}\n\n`); clients.add(res); req.on("close", () => clients.delete(res)); return;
        }
        if (req.method !== "POST") return json(res, 404, { error: "not found" });
        const body = await readBody(req);
        if (requestUrl.pathname === "/submit") {
          const selectedUrls = Array.isArray(body.selectedUrls) ? body.selectedUrls.filter((x) => typeof x === "string") : [];
          finish({ status: "submitted", selectedUrls }); return json(res, 202, { ok: true });
        }
        if (requestUrl.pathname === "/cancel") { finish({ status: "cancelled", selectedUrls: [] }); return json(res, 202, { ok: true }); }
        if (requestUrl.pathname === "/add-search") {
          const query = compactText(body.query, 500);
          if (!query) return json(res, 400, { error: "query is empty" });
          const extra = await onAddSearch(query);
          state.queries.push(query); state.results = publicResults(extra.results || state.results); state.responseId = extra.responseId || state.responseId; broadcast(); return json(res, 200, state);
        }
        if (requestUrl.pathname === "/rewrite") {
          const query = compactText(body.query, 500);
          const rewritten = await onRewrite(query);
          return json(res, 200, { query: rewritten });
        }
        if (requestUrl.pathname === "/summarize") {
          const selectedUrls = Array.isArray(body.selectedUrls) ? body.selectedUrls.filter((x) => typeof x === "string") : [];
          const summary = await onSummarize(selectedUrls);
          state.summary = summary.text || ""; state.summaryMeta = summary.meta; broadcast(); return json(res, 200, state);
        }
        return json(res, 404, { error: "not found" });
      } catch (error) { return json(res, 422, { error: compactText(error instanceof Error ? error.message : String(error), 400) }); }
    });
    await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
  } catch (error) {
    if (server) server.close();
    throw new Error(`curator-start-failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : 0;
  const url = `http://127.0.0.1:${port}/curator?token=${encodeURIComponent(token)}`;
  if (config.autoOpenBrowser !== false) openBrowser(url);
  timer = setTimeout(() => finish({ status: "timeout", selectedUrls: [] }), config.curatorTimeoutSeconds * 1000);
  return {
    url,
    state,
    wait: finishedPromise.finally(() => { clearTimeout(timer); server.close(); }),
    close: () => finish({ status: "cancelled", selectedUrls: [] }),
  };
}
