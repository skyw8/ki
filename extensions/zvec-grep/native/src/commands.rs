use crate::{
    App, cli,
    config::{Config, string},
    rpc::Host,
};
use serde_json::{Value, json};
use std::{
    path::{Path, PathBuf},
    sync::Arc,
    time::{Duration, Instant},
};

#[derive(Clone)]
pub struct Job {
    pub root: String,
    pub started: Instant,
    pub progress: Option<Value>,
}
pub fn specs() -> Vec<Value> {
    let names = ["zg-index", "zg-status", "zg-remove"];
    [("zg-index","Build or update the zvec-grep index for a workspace","[root] [--rebuild] [zg index options]"),("zg-status","Show the zvec-grep index status for a workspace","[root]"),("zg-remove","Delete the zvec-grep index of a workspace","[root]")].iter().map(|(name,description,hint)|json!({"name":name,"description":description,"argumentHint":hint,"completions":names.iter().filter(|n|*n!=name).collect::<Vec<_>>()})).collect()
}
pub fn clean(path: PathBuf) -> PathBuf {
    let mut clean = PathBuf::new();
    for part in path.components() {
        match part {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                clean.pop();
            }
            _ => clean.push(part.as_os_str()),
        }
    }
    clean
}
pub fn target(args: &str, cwd: &str, options: bool) -> Result<(String, Vec<String>), String> {
    let tokens = args.split_whitespace().collect::<Vec<_>>();
    let mut root = "";
    let mut extra = vec![];
    let mut i = 0;
    while i < tokens.len() {
        let token = tokens[i];
        if token.starts_with('-') {
            if !options {
                return Err(format!("{token} is not supported here"));
            }
            if token == "--drop" || token == "--yes" {
                return Err(
                    "use /zg-remove to delete an index; --drop is not accepted here".into(),
                );
            }
            extra.push(token.into());
            if !token.contains('=') && tokens.get(i + 1).is_some_and(|s| !s.starts_with('-')) {
                i += 1;
                extra.push(tokens[i].into());
            }
        } else if root.is_empty() {
            root = token;
        } else {
            return Err(format!("unexpected argument: {token}"));
        }
        i += 1;
    }
    let path = if Path::new(root).is_absolute() {
        PathBuf::from(root)
    } else {
        Path::new(cwd).join(if root.is_empty() { "." } else { root })
    };
    Ok((clean(path).to_string_lossy().into_owned(), extra))
}
fn outcome(notice: String) -> Value {
    json!({"handled":true,"notice":notice})
}
fn setting_args(c: &Config, args: &[String]) -> Vec<String> {
    let flags = args
        .iter()
        .map(|s| s.split('=').next().unwrap())
        .collect::<Vec<_>>();
    let mut setting = vec![];
    if !c.embedding.is_empty() && !flags.contains(&"--embedding") {
        setting.extend(["--embedding".into(), c.embedding.clone()]);
    }
    if c.local() && !flags.contains(&"--device") {
        setting.extend(["--device".into(), c.device.clone()]);
    }
    setting
}
fn job_line(job: &Job) -> String {
    if let Some(p) = &job.progress {
        if p["phase"] == "indexing" && p["total"].as_u64().unwrap_or(0) > 0 {
            return format!(
                "Index job running for {}: {}/{} files.",
                job.root,
                p["done"].as_u64().unwrap_or(0),
                p["total"]
            );
        }
        return format!(
            "Index job running for {} ({}).",
            job.root,
            string(&p["phase"])
        );
    }
    format!("Index job running for {}.", job.root)
}
pub async fn report(host: &Host, progress: &Value) {
    let text = match string(&progress["phase"]) {
        "scanning" => json!({"key":"index.scanning","fallback":"Scanning files…"}),
        "model" => {
            json!({"key":"index.modelDownload","params":{"model":progress["model"].as_str().unwrap_or("local")},"fallback":format!("Preparing embedding model {}",string(&progress["model"])).trim()})
        }
        "indexing" if progress["total"].as_u64().unwrap_or(0) > 0 => {
            json!({"key":"index.progress","params":{"done":progress["done"].as_u64().unwrap_or(0),"total":progress["total"],"failed":progress["failed"].as_u64().unwrap_or(0)},"fallback":format!("Indexing {}/{} files",progress["done"].as_u64().unwrap_or(0),progress["total"])})
        }
        _ => return,
    };
    host.status(text, "info").await;
}
fn first_number(s: &str) -> Option<u64> {
    s.split(|c: char| !c.is_ascii_digit())
        .find(|s| !s.is_empty())
        .and_then(|s| s.parse().ok())
}
pub fn index_error_hint(error: &str) -> String {
    // Native format/version and model errors name a CLI rebuild. Ki users need
    // the exact slash command: ordinary /zg-index only updates an existing index.
    if error.to_lowercase().contains("rebuild") && !error.contains("/zg-index --rebuild") {
        format!("{error}\nRun /zg-index --rebuild to rebuild it in Ki.")
    } else {
        error.into()
    }
}
fn success_status_lifetime() -> Duration {
    // Only the protocol test seam may shorten the real completion lifetime.
    if std::env::var_os("KI_ZVEC_GREP_TEST_STATE").is_some() {
        if let Some(ms) = std::env::var("KI_ZVEC_GREP_TEST_STATUS_CLEAR_MS")
            .ok()
            .and_then(|s| s.parse().ok())
        {
            return Duration::from_millis(ms);
        }
    }
    Duration::from_secs(30)
}
async fn finished(app: Arc<App>, host: Host, job: Job, c: Config, result: cli::Output) {
    let summary = result
        .stdout
        .lines()
        .filter_map(|s| s.split_once('\t'))
        .collect::<std::collections::HashMap<_, _>>();
    let files = summary.get("files").and_then(|s| first_number(s));
    let entities = summary.get("entities").and_then(|s| first_number(s));
    let seconds = summary.get("duration").and_then(|s| first_number(s));
    let ok = result.code == Some(0);
    let text = if ok {
        result.stdout.trim()
    } else if !result.stderr.is_empty() {
        result.stderr.trim()
    } else {
        result.stdout.trim()
    };
    let status = if ok {
        let seconds = seconds.unwrap_or_else(|| job.started.elapsed().as_secs_f64().round() as u64);
        json!({"key":"index.done","params":{"root":job.root,"files":files.unwrap_or(0),"entities":entities.unwrap_or(0),"seconds":seconds},"fallback":format!("Index ready for {}: {} files, {} entities in {seconds}s",job.root,files.unwrap_or(0),entities.unwrap_or(0))})
    } else {
        let mut error = text.lines().rev().take(3).collect::<Vec<_>>();
        error.reverse();
        let mut error = error.join(" ");
        if error.is_empty() {
            error = "unknown error".into();
        }
        let error = index_error_hint(&error);
        json!({"key":"index.failed","params":{"root":job.root,"error":error},"fallback":format!("Index failed for {}: {error}",job.root)})
    };
    host.status(status, if ok { "success" } else { "error" })
        .await;
    // Errors must remain visible until another index operation replaces them;
    // clearing them on a timer exposes the healthy-sidecar green fallback.
    if ok {
        let clear = host.clone();
        let timer = tokio::spawn(async move {
            tokio::time::sleep(success_status_lifetime()).await;
            clear.status(json!(""), "info").await;
        });
        app.status_timers
            .lock()
            .unwrap()
            .insert(host.session.clone(), timer.abort_handle());
    }
    app.jobs.lock().unwrap().remove(&host.session);
    if let Err(e)=host.call("session.appendEntry",json!({"customType":"zvec-grep-index","data":{"root":job.root,"ok":ok,"files":files,"entities":entities,"seconds":seconds}})).await{eprintln!("zvec-grep appendEntry: {e}");}
    if ok && c.notify {
        let text = format!(
            "zvec-grep: the index for {} is ready ({} files). zvec_grep_search can now answer semantic questions about it.",
            job.root,
            files.unwrap_or(0)
        );
        if let Err(e)=host.call("session.enqueue",json!({"content":[{"type":"text","text":text}],"deliverAs":"queue","when":"settled","idempotencyKey":format!("zvec-grep-index:{}",job.root),"kind":"custom","display":true})).await{eprintln!("zvec-grep enqueue: {e}");}
    }
}
pub async fn invoke(app: Arc<App>, params: Value) -> Result<Value, String> {
    let session = string(&params["sessionId"]);
    let name = string(&params["name"]);
    let host = app.host(session);
    let cwd = app.cwd(session);
    if !["zg-index", "zg-status", "zg-remove"].contains(&name) {
        return Ok(json!({"handled":false}));
    }
    let (root, mut extra) = match target(string(&params["args"]), &cwd, name == "zg-index") {
        Ok(v) => v,
        Err(e) => return Ok(outcome(e)),
    };
    if name == "zg-status" {
        let mut lines = vec![];
        if let Some(job) = app.jobs.lock().unwrap().get(session) {
            lines.push(job_line(job));
        }
        let result = cli::run_short(
            &[
                "status".into(),
                root.clone(),
                "--mode".into(),
                "direct".into(),
            ],
            &root,
        )
        .await;
        let text = if result.stdout.is_empty() {
            result.stderr.trim()
        } else {
            result.stdout.trim()
        };
        lines.push(if text.is_empty() {
            format!("zg status failed (exit {}).", cli::code_text(result.code))
        } else {
            text.into()
        });
        return Ok(outcome(lines.join("\n")));
    }
    if name == "zg-remove" {
        if app.jobs.lock().unwrap().contains_key(session) {
            return Ok(outcome("An index job is running for this session; wait for it to finish before removing the index.".into()));
        }
        let response=host.call("ui.confirm",json!({"title":{"key":"confirm.drop.title","fallback":"Remove the zvec-grep index?"},"message":{"key":"confirm.drop.message","params":{"root":root},"fallback":format!("This deletes {}.",Path::new(&root).join(".zvec-grep").display())}})).await?;
        if response["ok"] != true {
            return Ok(outcome("Cancelled; the index was not removed.".into()));
        }
        let result = cli::run_short(
            &[
                "index".into(),
                root.clone(),
                "--drop".into(),
                "--yes".into(),
                "--mode".into(),
                "direct".into(),
            ],
            &root,
        )
        .await;
        let text = if result.stdout.is_empty() {
            result.stderr.trim()
        } else {
            result.stdout.trim()
        };
        return Ok(outcome(if result.code != Some(0) {
            format!(
                "zg index --drop failed (exit {}):\n{text}",
                cli::code_text(result.code)
            )
        } else if text.is_empty() {
            format!("Removed the zvec-grep index for {root}.")
        } else {
            text.into()
        }));
    }
    let rebuild = extra.iter().any(|s| s == "--rebuild");
    extra.retain(|s| s != "--rebuild");
    let c = app.config().unwrap_or_default();
    let setting = setting_args(&c, &extra);
    let job = Job {
        root: root.clone(),
        started: Instant::now(),
        progress: None,
    };
    {
        let mut jobs = app.jobs.lock().unwrap();
        if let Some(job) = jobs.get(session) {
            return Ok(outcome(format!(
                "An index job is already running for {}.",
                job.root
            )));
        }
        jobs.insert(session.into(), job.clone());
        // An earlier successful job's expiry must not erase this job's progress
        // or a later failure. Abort before publishing the new starting status.
        if let Some(timer) = app.status_timers.lock().unwrap().remove(session) {
            timer.abort();
        }
    }
    let mut args = vec!["index".into(), root.clone()];
    args.extend(setting.iter().cloned());
    args.extend(extra.iter().cloned());
    args.extend(["--mode".into(), "direct".into()]);
    if rebuild {
        args.push("--rebuild".into());
    }
    let notice = format!(
        "Started `zg index {}` in the background. Progress appears on the extension status chip.",
        std::iter::once(root.clone())
            .chain(setting)
            .chain(extra)
            .chain(rebuild.then(|| "--rebuild".into()))
            .collect::<Vec<_>>()
            .join(" ")
    );
    let runner = app.clone();
    tokio::spawn(async move {
        host.status(json!({"key":"index.started","params":{"root":root},"fallback":format!("Index job started for {root}")}),"info").await;
        let result = cli::run_index(&args, &root, runner.clone(), host.clone()).await;
        finished(runner, host, job, c, result).await;
    });
    Ok(outcome(notice))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn command_flags_override_settings_and_remove_requires_its_own_command() {
        let root = std::env::temp_dir().to_string_lossy().into_owned();
        let (parsed, args) = target(
            "--embedding local/other --device=metal --rebuild",
            &root,
            true,
        )
        .unwrap();
        // macOS and Windows temp directories can retain a trailing separator;
        // target normalizes it, so compare paths rather than their spelling.
        assert_eq!(Path::new(&parsed), Path::new(&root));
        assert_eq!(
            args,
            ["--embedding", "local/other", "--device=metal", "--rebuild"]
        );
        assert!(setting_args(&Config::default(), &args).is_empty());
        assert!(
            target("--drop", &root, true)
                .unwrap_err()
                .contains("use /zg-remove")
        );
        assert!(target("--rebuild", &root, false).is_err());
    }

    #[test]
    fn command_target_normalizes_trailing_workspace_separators() {
        let root = std::env::temp_dir();
        let expected = clean(root.clone()).to_string_lossy().into_owned();
        let separator = std::path::MAIN_SEPARATOR.to_string();
        for suffix in ["", separator.as_str()] {
            let cwd = format!("{}{suffix}", root.display());
            let (parsed, args) = target("", &cwd, false).unwrap();
            assert_eq!(parsed, expected);
            assert!(args.is_empty());
        }
    }
}
