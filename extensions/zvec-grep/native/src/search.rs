use crate::{
    App,
    config::{Config, clamp, number, string, strings, trim},
    render,
};
use serde_json::{Value, json};
use std::{
    fs::OpenOptions,
    io::Write,
    path::Path,
    sync::{Arc, atomic::Ordering},
    time::{Duration, Instant},
};
use tokio::sync::Mutex as AsyncMutex;
use tokio_util::sync::CancellationToken;
use zg_engine::{
    ZvecGrep,
    api::context::{
        ContextOptions,
        options::{
            ContextRoute, ContextRouteMode, GlobRule, QueryFilter, RefreshPolicy, SymbolType,
        },
        result::{ContentRange, ContextResult},
    },
};

const WRITE_BUSY: &str = "ZVEC_GREP.ENGINE.DAEMON_LEASE_ACTIVE";
const LOCK_BUSY: &str = "ZVEC_GREP.ENGINE.LOCK.BUSY";
const NOT_FOUND: &str = "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_NOT_FOUND";
const DISABLED: &str = "ZVEC_GREP.ENGINE.SERVICE.WORKSPACE_INDEX_DISABLED";
#[derive(Debug)]
pub struct Failure {
    pub code: String,
    pub message: String,
    pub details: String,
}
impl Failure {
    fn new(code: &str, message: &str, details: String) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
            details,
        }
    }
}
pub fn log(value: Value) {
    if let Ok(path) = std::env::var("KI_ZVEC_GREP_TEST_LOG") {
        if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(path) {
            let _ = writeln!(f, "{value}");
        }
    }
}
pub fn error_result(text: &str) -> Value {
    json!({"content":[{"type":"text","text":text}],"isError":true})
}
pub fn cancelled(root: &str) -> Value {
    json!({"content":[{"type":"text","text":format!("zvec_grep_search for {root} was cancelled. No partial ranking is available because the index search is a single call; use Grep for an exact anchor or retry with a narrower query.")}],"details":{"root":root,"cancelled":true},"isError":true})
}
#[derive(Clone, Debug)]
pub struct Plan {
    pub root: String,
    pub queries: Vec<String>,
    pub fts: Vec<String>,
    pub vector: Vec<String>,
    pub fuse: bool,
    pub limit: u64,
    pub full: bool,
    pub eventual: bool,
    pub extra: Value,
}
fn modified(v: &Value) -> Result<Option<i64>, ()> {
    if v.is_null() || v == "" {
        return Ok(None);
    }
    if v.is_number() {
        return v
            .as_f64()
            .filter(|n| n.is_finite())
            .map(|n| Some(n.floor() as i64))
            .ok_or(());
    }
    if let Some(s) = v.as_str() {
        let s = trim(s);
        if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(s) {
            return Ok(Some(dt.timestamp_millis()));
        }
        if let Ok(d) = chrono::NaiveDate::parse_from_str(s, "%Y-%m-%d") {
            return Ok(Some(
                d.and_hms_opt(0, 0, 0).unwrap().and_utc().timestamp_millis(),
            ));
        }
    }
    Err(())
}
pub fn normalize(args: &Value, cwd: &str, c: &Config) -> Result<Plan, String> {
    let root = if trim(string(&args["root"])).is_empty() {
        cwd
    } else {
        trim(string(&args["root"]))
    };
    if !Path::new(root).is_absolute() {
        return Err(format!("root must be an absolute path: {root}"));
    }
    match std::fs::metadata(root) {
        Ok(m) if !m.is_dir() => return Err(format!("root is not a directory: {root}")),
        Err(_) => return Err(format!("root does not exist: {root}")),
        _ => {}
    }
    let mut queries = strings(&args["queries"]);
    let query = trim(string(&args["query"]));
    if !query.is_empty() {
        queries.insert(0, query.into());
    }
    let fts = strings(&args["fts"]);
    let vector = strings(&args["vector"]);
    if queries.is_empty() && fts.is_empty() && vector.is_empty() {
        return Err("zvec_grep_search requires query, queries, fts, or vector".into());
    }
    if queries.len() + fts.len() + vector.len() > 24 {
        return Err("too many query groups (max 8 per kind)".into());
    }
    let mut extra = json!({});
    for key in [
        "globs",
        "insensitiveGlobs",
        "fileTypes",
        "excludedFileTypes",
        "ignoreFiles",
    ] {
        let list = strings(&args[key]);
        if !list.is_empty() {
            extra[key] = json!(list);
        }
    }
    for key in ["hidden", "noIgnore", "preferSymbol"] {
        if args[key] == true {
            extra[key] = json!(true);
        }
    }
    let types = strings(&args["symbolTypes"])
        .into_iter()
        .filter(|s| {
            ["module", "class", "interface", "function", "value", "alias"].contains(&s.as_str())
        })
        .collect::<Vec<_>>();
    if !types.is_empty() {
        extra["symbolTypes"] = json!(types);
    }
    for (key, min) in [("maxDepth", 0.0), ("maxFileSizeBytes", 1.0)] {
        if let Some(n) = args
            .get(key)
            .and_then(number)
            .filter(|n| n.is_finite() && n.floor() >= min)
        {
            extra[key] = json!(n.floor() as u64);
        }
    }
    for key in ["modifiedAfter", "modifiedBefore"] {
        match modified(&args[key]) {
            Ok(Some(n)) => extra[key] = json!(n),
            Ok(None) => {}
            Err(()) => return Err(format!("{key} must be an ISO date or epoch milliseconds")),
        }
    }
    Ok(Plan {
        root: root.into(),
        queries,
        fts,
        vector,
        fuse: args["fuse"] == true,
        limit: clamp(args.get("limit"), 1, 50, c.max_results),
        full: args["preview"] == "full",
        eventual: args["freshness"] == "eventual",
        extra,
    })
}
pub fn context_options(p: &Plan, c: &Config, auto_update: bool) -> Value {
    let mut options = p.extra.clone();
    options["root"] = json!(p.root);
    if !p.queries.is_empty() {
        options["queries"] = json!(p.queries);
    }
    options["routes"] = json!(
        p.fts
            .iter()
            .map(|q| json!({"mode":"fts","query":q}))
            .chain(p.vector.iter().map(|q| json!({"mode":"vector","query":q})))
            .collect::<Vec<_>>()
    );
    options["fuse"] = json!(p.fuse);
    options["limit"] = json!(p.limit);
    options["autoUpdate"] = json!(auto_update);
    if !c.ignored_globs.is_empty() {
        options["excludePaths"] = json!(c.ignored_globs);
    }
    options
}
fn engine(app: &App, c: &Config) -> ZvecGrep {
    let key = format!("{}:{}", c.embedding, c.device);
    let mut saved = app.engine.lock().unwrap();
    if let Some((old, engine)) = saved.as_ref() {
        if *old == key {
            return engine.clone();
        }
        engine.close();
        log(json!({"event":"close","options":{}}));
    }
    let engine = ZvecGrep::new();
    let mut options = json!({"embedding":c.embedding});
    if c.local() {
        options["device"] = json!(c.device);
    }
    log(json!({"event":"create","options":options}));
    *saved = Some((key, engine.clone()));
    engine
}
async fn fake_context(app: &App, options: &Value) -> Result<Value, Failure> {
    log(json!({"event":"context","options":options}));
    let state = std::env::var("KI_ZVEC_GREP_TEST_STATE")
        .ok()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .and_then(|s| serde_json::from_str::<Value>(&s).ok())
        .unwrap_or(json!({}));
    let root = string(&options["root"]);
    let refreshing = options["autoUpdate"] != false;
    if std::env::var("KI_ZVEC_GREP_TEST_HANG").as_deref() == Ok("1") {
        tokio::time::sleep(Duration::from_secs(60)).await;
    }
    let write = string(&state["indexWrite"]);
    if write == "daemon" || write == "always" || (write == "contended" && refreshing) {
        let pid = if write == "daemon" { 4242 } else { 0 };
        return Err(Failure::new(
            WRITE_BUSY,
            "A zvec-grep daemon owns index writes for this root",
            format!(
                "root={root}\npid={pid}\nhint=Run with --mode auto so a ready daemon handles indexed operations.\nconfig=Edit ~/.zvec-grep/config.json and set client.mode to \"auto\" to persist this behavior."
            ),
        ));
    }
    let lock_busy = |operation: &str| {
        Failure::new(
            LOCK_BUSY,
            "Index unavailable",
            format!(
                "lock={root}/.zvec-grep/locks/home.write\noperation=context\nownerOperation={operation}\nownerPid=5150\nownerHost=stub"
            ),
        )
    };
    if let Some(ms) = state["holdWriterLockMs"].as_u64() {
        if app.fake_locks.load(Ordering::SeqCst) > 0 {
            return Err(lock_busy("context.refresh"));
        }
        if refreshing {
            app.fake_locks.fetch_add(1, Ordering::SeqCst);
            tokio::time::sleep(Duration::from_millis(ms)).await;
            app.fake_locks.fetch_sub(1, Ordering::SeqCst);
        }
    }
    if !string(&state["lockBusy"]).is_empty()
        && (state["lockBusy"] == "always" || app.fake_calls.fetch_add(1, Ordering::SeqCst) == 0)
    {
        return Err(lock_busy(
            state["lockOwnerOperation"].as_str().unwrap_or("index"),
        ));
    }
    if state["indexed"] == false {
        return Err(Failure::new(
            NOT_FOUND,
            "No zvec-grep index found for this workspace",
            "hint=built by the test stub".into(),
        ));
    }
    if state["disabled"] == true {
        return Err(Failure::new(
            DISABLED,
            "The zvec-grep index is disabled for this workspace",
            "hint=built by the test stub".into(),
        ));
    }
    if let Some(message) = state["failure"].as_str() {
        return Err(Failure::new("", message, "".into()));
    }
    let items=state["items"].as_array().map(|items|items.iter().enumerate().map(|(i,v)|{
        let mut item=json!({"kind":"indexed_entity","rank":i+1,"file":{"absolutePath":Path::new(root).join(string(&v["path"])),"relativePath":v["path"]},"range":{"kind":"text","startLine":v["startLine"].as_u64().unwrap_or(1),"endLine":v["endLine"].as_u64().unwrap_or(1)},"content":v["content"].as_str().unwrap_or(""),"status":v["status"].as_str().unwrap_or("fresh"),"matchedBy":v["matchedBy"].as_str().unwrap_or("fts")});
        for key in ["outline","score","metadata"] {if let Some(value)=v.get(key){item[key]=value.clone();}}item
    }).collect::<Vec<_>>()).unwrap_or_default();
    let query = options["queries"][0].as_str().unwrap_or("?");
    Ok(
        json!({"query":query,"root":root,"source":"index","coverage":"ranked_sample","workspaceIndex":{"id":"ws-1","name":"stub","path":"/tmp/zvec-home"},"items":items,"diagnostics":{"index":{"hitsReturned":items.len(),"routes":[{"id":"fts","mode":"fts","query":query}]}}}),
    )
}
fn range(range: &ContentRange) -> Value {
    json!({"kind":"text","startLine":range.start_line().unwrap_or(1),"endLine":range.last_line().unwrap_or(1)})
}
fn camel(value: &Value) -> Value {
    match value {
        Value::Array(v) => Value::Array(v.iter().map(camel).collect()),
        Value::Object(v) => Value::Object(
            v.iter()
                .map(|(key, value)| {
                    let mut parts = key.split('_');
                    let mut name = parts.next().unwrap().to_owned();
                    for part in parts {
                        let mut chars = part.chars();
                        if let Some(c) = chars.next() {
                            name.extend(c.to_uppercase());
                        }
                        name.extend(chars);
                    }
                    (name, camel(value))
                })
                .collect(),
        ),
        _ => value.clone(),
    }
}
pub fn legacy(result: ContextResult) -> Value {
    let mut value = camel(&serde_json::to_value(&result).unwrap());
    for (v, item) in value["items"]
        .as_array_mut()
        .unwrap()
        .iter_mut()
        .zip(&result.items)
    {
        v["file"] = json!({"absolutePath":item.absolute_path,"relativePath":item.relative_path});
        v["range"] = range(&item.range);
        // Native ranges are half-open; render uses inclusive file:line anchors.
        v["contentStartLine"] = json!(item.content_range.start_line().unwrap_or(1));
        if let Some(container) = &item.container {
            v["container"]["range"] = range(&container.range);
        }
        if let Some(name) = v["metadata"]["symbolName"].as_str().map(str::to_owned) {
            v["metadata"]["name"] = json!(name);
        }
    }
    if !value["workspaceIndex"].is_null() {
        value["workspaceIndex"]["id"] = json!(
            result
                .workspace_index
                .as_ref()
                .unwrap()
                .path
                .to_string_lossy()
        );
    }
    value
}
async fn context(
    app: &App,
    p: &Plan,
    c: &Config,
    update: bool,
    signal: CancellationToken,
) -> Result<Value, Failure> {
    let engine = engine(app, c);
    let opts = context_options(p, c, update);
    if std::env::var_os("KI_ZVEC_GREP_TEST_STATE").is_some() {
        return fake_context(app, &opts).await;
    }
    let mut globs = strings(&opts["globs"])
        .into_iter()
        .map(GlobRule::from)
        .collect::<Vec<_>>();
    globs.extend(
        strings(&opts["insensitiveGlobs"])
            .into_iter()
            .map(|pattern| GlobRule {
                pattern,
                case_insensitive: true,
            }),
    );
    globs.extend(
        c.ignored_globs
            .iter()
            .map(|s| GlobRule::from(format!("!{}", s.trim_start_matches('!')))),
    );
    let options = ContextOptions {
        root: Some(p.root.clone().into()),
        queries: p.queries.clone(),
        routes: p
            .fts
            .iter()
            .map(|q| ContextRoute {
                mode: ContextRouteMode::Fts,
                query: q.clone(),
            })
            .chain(p.vector.iter().map(|q| ContextRoute {
                mode: ContextRouteMode::Vector,
                query: q.clone(),
            }))
            .collect(),
        fuse: p.fuse,
        limit: Some(p.limit as usize),
        auto_update: update,
        refresh: Some(if update {
            RefreshPolicy::Wait
        } else {
            RefreshPolicy::Off
        }),
        prefer_symbol: opts["preferSymbol"] == true,
        filter: QueryFilter {
            globs,
            file_types: strings(&opts["fileTypes"]),
            excluded_file_types: strings(&opts["excludedFileTypes"]),
            modified_after_epoch_ms: opts["modifiedAfter"].as_u64(),
            modified_before_epoch_ms: opts["modifiedBefore"].as_u64(),
            symbol_types: strings(&opts["symbolTypes"])
                .iter()
                .filter_map(|s| serde_json::from_value::<SymbolType>(json!(s)).ok())
                .collect(),
            ..Default::default()
        },
        authorization_model: Some(c.embedding.clone()),
        device: if c.local() {
            serde_json::from_value(json!(c.device)).ok()
        } else {
            None
        },
        signal: Some(signal),
        lock_timeout_ms: Some(400),
        ..Default::default()
    };
    engine.context(options).await.map(legacy).map_err(|e| {
        let code = match e.code() {
            zg_engine::EngineError::NOT_FOUND if e.message().contains("workspace index") => {
                NOT_FOUND
            }
            // The native engine describes disabled policy as unsupported; keep the tool's explicit do-not-build result.
            zg_engine::EngineError::UNSUPPORTED if e.message().contains("indexing is disabled") => {
                DISABLED
            }
            zg_engine::EngineError::RESOURCE_BUSY => LOCK_BUSY,
            zg_engine::EngineError::DEADLINE_EXCEEDED => LOCK_BUSY,
            _ => e.code(),
        };
        Failure::new(code, e.message(), e.help().unwrap_or("").into())
    })
}
#[derive(Default)]
struct Outcome {
    skip: Option<&'static str>,
    holder: Option<u64>,
    attempts: usize,
}
fn field<'a>(details: &'a str, name: &str) -> Option<&'a str> {
    details
        .lines()
        .find_map(|line| line.strip_prefix(&format!("{name}=")).map(str::trim))
        .filter(|s| !s.is_empty())
}
fn holder(details: &str) -> Option<u64> {
    field(details, "pid")
        .and_then(|s| s.parse().ok())
        .filter(|n| *n > 0)
}
fn details(p: &Plan, start: Instant, o: &Outcome) -> Value {
    json!({"root":p.root,"durationMs":start.elapsed().as_millis() as u64,"refreshSkipped":o.skip,"attempts":o.attempts})
}
fn result_error(text: String, details: Value) -> Value {
    json!({"content":[{"type":"text","text":text}],"details":details,"isError":true})
}
fn failure(e: Failure, p: &Plan, start: Instant, o: &Outcome, job: bool) -> Value {
    let mut d = details(p, start, o);
    d["code"] = if e.code.is_empty() {
        Value::Null
    } else {
        json!(e.code)
    };
    if (e.code == WRITE_BUSY || e.code == LOCK_BUSY) && job {
        d["reason"] = json!("index_job");
        d["retryable"] = json!(true);
        return result_error(
            format!(
                "zvec_grep_search cannot read the index of {} while an index job is building it: `zg index` holds the workspace write lock for the whole build, and zg's read path refuses to run alongside a writer.\nWait for the job to finish (progress is on the extension status chip) and search again; use Grep for an exact anchor in the meantime.",
                p.root
            ),
            d,
        );
    }
    if e.code == LOCK_BUSY {
        let operation = field(&e.details, "ownerOperation");
        let pid = field(&e.details, "ownerPid")
            .and_then(|s| s.parse::<u64>().ok())
            .filter(|n| *n > 0);
        d["holder"] = json!(operation.unwrap_or("unknown"));
        d["holderPid"] = json!(pid);
        d["retryable"] = json!(true);
        let by = operation
            .map(|s| {
                format!(
                    " by `{s}`{}",
                    pid.map(|pid| format!(" (pid {pid})")).unwrap_or_default()
                )
            })
            .unwrap_or_default();
        return result_error(
            format!(
                "zvec_grep_search failed: another writer holds the zvec-grep workspace write lock of {}{by}.\nlock={}\nAnother `zg index`, `zg server`, or ki session is writing that index, and zg's read path refuses to run alongside a writer. Retry once that writer finishes, or use Grep for an exact anchor.",
                p.root,
                field(&e.details, "lock").unwrap_or("unknown")
            ),
            d,
        );
    }
    if e.code == WRITE_BUSY {
        let pid = o.holder.or_else(|| holder(&e.details));
        d["holderPid"] = json!(pid);
        if let Some(pid) = pid {
            d["holder"] = json!("daemon");
            return result_error(
                format!(
                    "zvec_grep_search failed: a zvec-grep daemon owns the index writes of {} (pid {pid}).\nThis extension searches the index in-process, so it cannot update an index a daemon owns. Tell the user to stop the daemon (`zg server off`) or to switch this extension's mode setting to `external-daemon`, which queries the daemon instead.",
                    p.root
                ),
                d,
            );
        }
        d["holder"] = json!("unknown");
        return result_error(
            format!(
                "zvec_grep_search failed: another writer holds the zvec-grep index write lock for {} (this extension's own index refresh or /zg-index, or another zg process).\nThe lock is released when that writer finishes, so retry the search; pass freshness=eventual to search without refreshing, or use Grep for an exact anchor.",
                p.root
            ),
            d,
        );
    }
    if e.code == NOT_FOUND {
        return result_error(
            format!(
                "No zvec-grep index for {}.\nThis tool never builds an index. Tell the user the workspace is not indexed and that /zg-index (or `zg index`) creates one; until then use Grep/Glob for exact lookups.",
                p.root
            ),
            d,
        );
    }
    if e.code == DISABLED {
        d["agentAction"] = json!("do_not_build_index");
        return result_error(
            format!(
                "The zvec-grep index of {} is disabled. Do not rebuild it; use Grep/Glob, or let the user re-enable it with /zg-index.",
                p.root
            ),
            d,
        );
    }
    if e.code == "timeout" {
        d.as_object_mut().unwrap().remove("code");
        d["timeout"] = json!(true);
        return result_error(
            format!(
                "zvec_grep_search timed out after {}ms for {}. The search may still be refreshing the index. Retry once, narrow the query or globs, or use Grep for an exact anchor.",
                start.elapsed().as_millis(),
                p.root
            ),
            d,
        );
    }
    result_error(
        format!(
            "zvec_grep_search failed: {}{}",
            e.message,
            if e.details.is_empty() {
                "".into()
            } else {
                format!("\n{}", e.details)
            }
        ),
        d,
    )
}
fn success(result: Value, p: &Plan, start: Instant, o: &Outcome) -> Value {
    let mut text = render::render(&result, p.full);
    if let Some(skip) = o.skip {
        let note = if skip == "index_job" {
            "note: refresh skipped — an index job is running for this root; results come from the index as last built".into()
        } else if let Some(pid) = o.holder {
            format!(
                "note: refresh skipped — a zvec-grep daemon owns index writes for this root (pid {pid}); results come from the index as last built — stop the daemon (`zg server off`) or switch this extension's mode setting to `external-daemon` if it must stay current"
            )
        } else {
            "note: refresh skipped — another writer holds the index write lock; results come from the index as last built".into()
        };
        text = format!("{note}\n{text}");
    }
    let items = result["items"].as_array().unwrap();
    let routes = result["diagnostics"]["index"]["routes"]
        .as_array()
        .map(|v| {
            v.iter()
                .map(|r| format!("{}:{}", string(&r["mode"]), string(&r["query"])))
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    json!({"content":[{"type":"text","text":text}],"details":{"root":result["root"],"source":result["source"],"coverage":result["coverage"],"items":items.len(),"workspaceIndex":result["workspaceIndex"]["id"],"stale":items.iter().filter(|v|v["status"]=="possibly_stale").count(),"emptyReason":result["diagnostics"]["emptyReason"],"routes":routes,"refreshSkipped":o.skip,"holderPid":o.holder,"attempts":o.attempts,"durationMs":start.elapsed().as_millis() as u64}})
}
pub async fn execute(
    app: Arc<App>,
    args: Value,
    cwd: String,
    signal: CancellationToken,
    key: String,
) -> Result<Value, String> {
    let c = app.config()?;
    let p = match normalize(&args, &cwd, &c) {
        Ok(p) => p,
        Err(e) => return Ok(error_result(&e)),
    };
    if let Some(pending) = app.active.lock().unwrap().get_mut(&key) {
        pending.root = p.root.clone();
    }
    if c.mode == "external-daemon" {
        return Ok(crate::cli::external_search(&p, &c).await);
    }
    let start = Instant::now();
    let deadline = tokio::time::Instant::now() + Duration::from_millis(c.timeout_ms);
    let gate = {
        let mut gates = app.gates.lock().unwrap();
        gates.retain(|_, v| v.strong_count() > 0);
        let gate = gates
            .get(&p.root)
            .and_then(std::sync::Weak::upgrade)
            .unwrap_or_else(|| Arc::new(AsyncMutex::new(())));
        gates.insert(p.root.clone(), Arc::downgrade(&gate));
        gate
    };
    let mut outcome = Outcome::default();
    let job = !p.eventual && app.job_active(&p.root);
    if job {
        outcome.skip = Some("index_job");
    }
    let mut update = !p.eventual && !job;
    let mut lock_waits = 0;
    let work = async {
        let _gate = gate.lock().await;
        loop {
            outcome.attempts += 1;
            let permit = app.slots.acquire().await.unwrap();
            let response = context(&app, &p, &c, update, signal.clone()).await;
            drop(permit);
            match response {
                Ok(value) => return success(value, &p, start, &outcome),
                Err(e) => {
                    if e.code != WRITE_BUSY && e.code != LOCK_BUSY {
                        return failure(e, &p, start, &outcome, false);
                    }
                    outcome.holder = holder(&e.details).or(outcome.holder);
                    if app.job_active(&p.root) {
                        return failure(e, &p, start, &outcome, true);
                    }
                    if e.code == WRITE_BUSY && update {
                        if outcome.attempts == 1 {
                            tokio::time::sleep(Duration::from_millis(250)).await;
                            continue;
                        }
                        update = false;
                        outcome.skip = Some("write_busy");
                        continue;
                    }
                    if e.code == LOCK_BUSY && lock_waits < 2 {
                        lock_waits += 1;
                        tokio::time::sleep(Duration::from_millis(400)).await;
                        continue;
                    }
                    return failure(e, &p, start, &outcome, false);
                }
            }
        }
    };
    tokio::select! {
        value=tokio::time::timeout_at(deadline,work)=>Ok(match value{Ok(v)=>v,Err(_)=>{signal.cancel();failure(Failure::new("timeout","","".into()),&p,start,&outcome,false)}}),
        _=signal.cancelled()=>Ok(cancelled(&p.root)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn search_limits_and_optional_integers_share_ecmascript_coercion() {
        let cwd = std::env::temp_dir().to_string_lossy().into_owned();
        let c = Config {
            max_results: 7,
            ..Config::default()
        };
        for (limit, expected) in [
            (json!(""), 1),
            (json!([]), 1),
            (json!([null]), 1),
            (json!(["0x10"]), 16),
            (json!("0b11"), 3),
            (json!([1, 2]), 7),
            (json!("\u{0085}3"), 7),
            (json!("\u{feff}2.9\u{2003}"), 2),
        ] {
            let p = normalize(&json!({"query":"\u{feff}intent\u{3000}","limit":limit,"maxDepth":[],"maxFileSizeBytes":["0o10"]}), &cwd, &c).unwrap();
            assert_eq!(p.limit, expected, "{limit}");
            assert_eq!(p.queries, ["intent"]);
            assert_eq!(p.extra["maxDepth"], 0);
            assert_eq!(p.extra["maxFileSizeBytes"], 8);
        }
        assert_eq!(normalize(&json!({"query":"x"}), &cwd, &c).unwrap().limit, 7);
    }
    #[test]
    fn query_filters_keep_dates_scope_and_user_exclusions() {
        let cwd = std::env::temp_dir().to_string_lossy().into_owned();
        let mut config = Config::default();
        config.ignored_globs = vec!["**/dist/**".into()];
        let plan=normalize(&json!({"query":" main query ","queries":["another"],"fts":["symbol"],"vector":["intent"],"globs":["src/**"],"insensitiveGlobs":["*.MD"],"hidden":true,"maxDepth":0,"modifiedAfter":"2026-10-01","modifiedBefore":1790870400000_i64,"symbolTypes":["function","invalid"],"limit":100,"fuse":true}),&cwd,&config).unwrap();
        let opts = context_options(&plan, &config, false);
        assert_eq!(opts["queries"], json!(["main query", "another"]));
        assert_eq!(opts["limit"], 50);
        assert_eq!(opts["excludePaths"], json!(["**/dist/**"]));
        assert_eq!(opts["modifiedAfter"], 1790812800000_i64);
        assert_eq!(opts["symbolTypes"], json!(["function"]));
        assert_eq!(opts["maxDepth"], 0);
        assert_eq!(opts["autoUpdate"], false);
        assert!(
            normalize(
                &json!({"query":"x","modifiedBefore":"invalid date"}),
                &cwd,
                &config
            )
            .is_err()
        );
        assert!(normalize(&json!({"query":"x","root":"relative/path"}), &cwd, &config).is_err());
    }
}
