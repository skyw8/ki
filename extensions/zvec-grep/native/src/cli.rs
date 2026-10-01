use crate::{
    App, commands,
    config::{Config, strings},
    rpc::Host,
    search::{Plan, error_result},
};
use serde_json::{Value, json};
use std::{ffi::OsString, io, process::Stdio, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncBufReadExt, BufReader},
    process::Command,
};
use zg_cli::{Cli, CliPlan, ClientMode, IndexOperation, ServerPlan, ServerStartArgs};
use zg_daemon_protocol::{DaemonCommand, DaemonReply};
use zg_engine::{
    ZvecGrep,
    api::index::progress::{IndexEmbeddingStage, IndexProgressPhase, IndexProgressReporter},
};

pub struct Output {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
}
pub fn code_text(code: Option<i32>) -> String {
    code.map(|n| n.to_string()).unwrap_or("null".into())
}
fn command(args: &[String], cwd: &str) -> Command {
    let mut command = if let Ok(path) = std::env::var("KI_ZVEC_GREP_CLI")
        .map(|s| s.trim().to_owned())
        .and_then(|s| {
            if s.is_empty() {
                Err(std::env::VarError::NotPresent)
            } else {
                Ok(s)
            }
        }) {
        Command::new(path)
    } else {
        let mut c = Command::new(std::env::current_exe().unwrap());
        c.arg("--ki-cli");
        c
    };
    command
        .args(args)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    command
}
pub async fn run_short(args: &[String], cwd: &str) -> Output {
    match tokio::time::timeout(Duration::from_secs(30), command(args, cwd).output()).await {
        Ok(Ok(output)) => Output {
            code: output.status.code(),
            stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
            stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
        },
        Ok(Err(e)) => Output {
            code: None,
            stdout: "".into(),
            stderr: e.to_string(),
        },
        Err(_) => Output {
            code: None,
            stdout: "".into(),
            stderr: "zg command timed out after 30000ms".into(),
        },
    }
}
pub fn progress_line(line: &str) -> Option<Value> {
    let s = line.trim();
    let lower = s.to_lowercase();
    if lower.starts_with("scanning files") {
        return Some(json!({"phase":"scanning","detail":s}));
    }
    if lower.starts_with("downloading ") || lower.starts_with("preparing ") {
        let model = s.split_once(' ').unwrap().1.split(" ·").next().unwrap();
        return Some(json!({"phase":"model","model":model}));
    }
    if lower.starts_with("indexing files") {
        let counts = s[14..].trim_start_matches(':').trim();
        let (done, rest) = counts.split_once('/')?;
        let end = rest
            .find(|c: char| !c.is_ascii_digit())
            .unwrap_or(rest.len());
        let done = done.parse::<u64>().ok()?;
        let total = rest[..end].parse::<u64>().ok()?;
        let failed = rest[end..]
            .split_once(" failed")
            .and_then(|(prefix, _)| {
                prefix
                    .rsplit(|c: char| !c.is_ascii_digit())
                    .find(|s| !s.is_empty())
                    .and_then(|s| s.parse::<u64>().ok())
            })
            .unwrap_or(0);
        return Some(json!({"phase":"indexing","done":done,"total":total,"failed":failed}));
    }
    if lower.starts_with("indexing complete") || lower.starts_with("indexing finished") {
        return Some(json!({"phase":"done","detail":s}));
    }
    None
}
pub async fn run_index(args: &[String], cwd: &str, app: Arc<App>, host: Host) -> Output {
    let mut child = match command(args, cwd).spawn() {
        Ok(c) => c,
        Err(e) => {
            return Output {
                code: None,
                stdout: "".into(),
                stderr: e.to_string(),
            };
        }
    };
    let stdout = child.stdout.take().unwrap();
    let stderr = child.stderr.take().unwrap();
    let consume = |stream: Box<dyn tokio::io::AsyncRead + Unpin + Send>,
                   app: Arc<App>,
                   host: Host| async move {
        let mut lines = BufReader::new(stream).lines();
        let mut text = String::new();
        while let Ok(Some(line)) = lines.next_line().await {
            text.push_str(&line);
            text.push('\n');
            if let Some(progress) = progress_line(&line) {
                if let Some(job) = app.jobs.lock().unwrap().get_mut(&host.session) {
                    job.progress = Some(progress.clone());
                }
                commands::report(&host, &progress).await;
            }
        }
        text
    };
    let (stdout, stderr, status) = tokio::join!(
        consume(Box::new(stdout), app.clone(), host.clone()),
        consume(Box::new(stderr), app, host),
        child.wait()
    );
    Output {
        code: status.ok().and_then(|s| s.code()),
        stdout,
        stderr,
    }
}
fn tail(text: &str, max: usize) -> String {
    let text = text.trim();
    let chars = text.chars().collect::<Vec<_>>();
    if chars.len() <= max {
        text.into()
    } else {
        format!(
            "…\n{}",
            chars[chars.len() - max..].iter().collect::<String>()
        )
    }
}
pub async fn external_search(p: &Plan, c: &Config) -> Value {
    let ready = run_short(
        &["server".into(), "status".into(), "--check-ready".into()],
        &p.root,
    )
    .await;
    if ready.code != Some(0) {
        return error_result(&format!(
            "zvec-grep external-daemon mode is selected but `zg server status --check-ready` failed. Start the daemon with `zg server on` or switch the extension setting mode back to in-process.\n{}",
            tail(
                if ready.stderr.is_empty() {
                    &ready.stdout
                } else {
                    &ready.stderr
                },
                4000
            )
        ));
    }
    let mut args = vec![
        "query".into(),
        "--mode".into(),
        "server".into(),
        "--limit".into(),
        p.limit.to_string(),
        "--preview".into(),
        if p.full { "full" } else { "short" }.into(),
    ];
    for (flag, queries) in [
        ("--hybrid", &p.queries),
        ("--fts", &p.fts),
        ("--vector", &p.vector),
    ] {
        for q in queries {
            args.extend([flag.into(), q.clone()]);
        }
    }
    if p.fuse {
        args.push("--fuse".into());
    }
    if p.eventual {
        args.extend(["--refresh".into(), "off".into()]);
    }
    for (key, flag) in [
        ("globs", "-g"),
        ("insensitiveGlobs", "--iglob"),
        ("fileTypes", "-t"),
        ("excludedFileTypes", "-T"),
    ] {
        for s in strings(&p.extra[key]) {
            args.extend([flag.into(), s]);
        }
    }
    for (key, flag) in [("hidden", "--hidden"), ("noIgnore", "--no-ignore")] {
        if p.extra[key] == true {
            args.push(flag.into());
        }
    }
    let result = run_short(&args, &p.root).await;
    let text = if result.stdout.is_empty() {
        result.stderr
    } else {
        result.stdout
    };
    if result.code != Some(0) {
        return json!({"content":[{"type":"text","text":format!("zvec_grep_search (external-daemon) failed: {}",tail(&text,4000))}],"details":{"root":p.root,"code":result.code,"mode":c.mode},"isError":true});
    }
    json!({"content":[{"type":"text","text":tail(&text,60000)}],"details":{"root":p.root,"source":"index-cli","mode":c.mode,"limit":p.limit}})
}
fn legacy_args(args: Vec<OsString>) -> Vec<OsString> {
    let mut args = args;
    if args.first().is_some_and(|s| s == "--ki-cli") {
        args.remove(0);
    }
    if let Some(first) = args.first().and_then(|s| s.to_str()) {
        if first == "query" {
            args.remove(0);
        } else if [
            "index",
            "status",
            "server",
            "config",
            "auth",
            "install",
            "uninstall",
        ]
        .contains(&first)
        {
            args[0] = format!("--{first}").into();
        }
    }
    let mut result = vec![OsString::from("zg")];
    result.extend(args);
    result
}
fn server_config(
    args: ServerStartArgs,
) -> Result<zg_daemon::ServerConfig, Box<dyn std::error::Error>> {
    zg_daemon::resolve_token(args.token_file.as_deref())?;
    let home = zg_daemon::resolve_home(args.home)?;
    let mut config = zg_daemon::ServerConfig::new(args.listen.parse()?, home);
    config.token_file = args.token_file;
    config.mcp_toolset = args.mcp_toolset.map(|s| match s {
        zg_cli::McpToolset::Agent => zg_daemon::McpToolset::Agent,
        zg_cli::McpToolset::Full => zg_daemon::McpToolset::Full,
    });
    Ok(config)
}
fn show_server(s: &zg_daemon::DaemonStatus) {
    println!(
        "Server: {}",
        if s.ready {
            "ready"
        } else if s.running {
            "starting"
        } else {
            "stopped"
        }
    );
    if let Some(pid) = s.pid {
        println!("PID: {pid}");
    }
    if let Some(url) = &s.server_url {
        println!("URL: {url}");
    }
}
async fn use_server(
    mode: ClientMode,
    home: Option<&std::path::Path>,
) -> Result<bool, Box<dyn std::error::Error>> {
    Ok(match mode {
        ClientMode::Direct => false,
        ClientMode::Server => true,
        ClientMode::Auto => {
            zg_daemon::server_status(&zg_daemon::resolve_home(
                home.map(std::path::Path::to_path_buf),
            )?)
            .await?
            .ready
        }
    })
}
fn mismatch() -> Box<dyn std::error::Error> {
    io::Error::other("zg daemon returned an unexpected response").into()
}
pub async fn run() -> Result<(), Box<dyn std::error::Error>> {
    if std::env::var_os("KI_ZVEC_GREP_TEST_STATE").is_some() {
        return fake_run().await;
    }
    let cli = Cli::try_parse_from(legacy_args(std::env::args_os().skip(1).collect()))?;
    let plan = cli.into_plan(std::env::current_dir()?)?;
    let engine = ZvecGrep::new();
    match plan {
        CliPlan::Help(topic) => zg_cli::print_help(topic.as_deref())?,
        CliPlan::Version => println!("zvec-grep Rust engine c1d2297"),
        CliPlan::Query {
            mode,
            home,
            request,
            output,
        } => {
            let mut request = *request;
            let server = use_server(mode, home.as_deref()).await?;
            zg_cli::finalize_refresh(&mut request, server);
            let result = if server {
                let home = zg_daemon::resolve_home(home)?;
                match zg_daemon::execute_command(&home, DaemonCommand::Context(request)).await? {
                    DaemonReply::Context(v) => *v,
                    _ => return Err(mismatch()),
                }
            } else {
                engine.context(request).await?
            };
            let text = crate::render::render(
                &crate::search::legacy(result),
                matches!(output.preview, zg_cli::PreviewMode::Full),
            );
            println!("{text}");
        }
        CliPlan::Status {
            mode,
            home,
            request,
            check_ready,
            output,
        } => {
            let result = if use_server(mode, home.as_deref()).await? {
                let home = zg_daemon::resolve_home(home)?;
                match zg_daemon::execute_command(&home, DaemonCommand::Info(request)).await? {
                    DaemonReply::Info(v) => *v,
                    _ => return Err(mismatch()),
                }
            } else {
                engine.info(request).await?
            };
            zg_cli::write_info_with_options(io::stdout().lock(), &result, output, false)?;
            if check_ready
                && result.index_status() != zg_engine::api::info::result::IndexStatus::Ready
            {
                return Err(io::Error::other("workspace index is not ready").into());
            }
        }
        CliPlan::Index {
            mode,
            home,
            operation,
            output: _,
        } => match operation {
            IndexOperation::Drop(request) => {
                let root = request.root.clone();
                let dropped = if use_server(mode, home.as_deref()).await? {
                    let home = zg_daemon::resolve_home(home)?;
                    match zg_daemon::execute_command(&home, DaemonCommand::DropIndex(request))
                        .await?
                    {
                        DaemonReply::DropIndex(v) => v,
                        _ => return Err(mismatch()),
                    }
                } else {
                    engine.drop_index(request).await?
                };
                println!(
                    "Workspace index: {}",
                    if dropped { "dropped" } else { "missing" }
                );
                if let Some(root) = root {
                    println!("Root: {}", root.display());
                }
            }
            IndexOperation::Build(request) => {
                let mut request = *request;
                let progress = IndexProgressReporter::new(|event| {
                    if let Some(e) = &event.embedding {
                        if matches!(
                            e.stage,
                            Some(IndexEmbeddingStage::Preparing | IndexEmbeddingStage::Downloading)
                        ) {
                            eprintln!("Preparing {}", e.model.as_deref().unwrap_or("local"));
                            return;
                        }
                    }
                    match event.phase {
                        IndexProgressPhase::Scanning => eprintln!("Scanning files..."),
                        IndexProgressPhase::Indexing => eprintln!(
                            "Indexing files: {}/{} {} failed",
                            event.files_indexed.unwrap_or(0),
                            event.files_total.unwrap_or(0),
                            event.files_failed.unwrap_or(0)
                        ),
                        IndexProgressPhase::Done => eprintln!("Indexing complete"),
                    }
                })
                .prioritize_model_progress();
                let result = if use_server(mode, home.as_deref()).await? {
                    let home = zg_daemon::resolve_home(home)?;
                    zg_daemon::index_with_progress(&home, request, &progress).await?
                } else {
                    request.on_progress = Some(progress);
                    engine.index(request).await?
                };
                println!(
                    "files\t{} scanned, {} added, {} modified, {} unchanged, {} deleted, {} failed",
                    result.files_scanned,
                    result.files_added,
                    result.files_modified,
                    result.files_unchanged,
                    result.files_deleted,
                    result.files_failed
                );
                println!("entities\t{}", result.entities_created);
                println!(
                    "duration\t{}s ({}ms)",
                    result.duration_micros / 1_000_000,
                    result.duration_micros / 1_000
                );
            }
        },
        CliPlan::Config(args) => {
            use zg_cli::{ConfigAction, ModelAction, ProviderAction};
            let path = match args.action {
                ConfigAction::Provider {
                    action: ProviderAction::Set { reference, api_key },
                } => zg_engine::config::set_provider(&reference, &api_key)?,
                ConfigAction::Model {
                    action:
                        ModelAction::Set {
                            reference,
                            endpoint,
                            device,
                            default_model,
                        },
                } => zg_engine::config::set_model(
                    &reference,
                    endpoint.as_deref(),
                    device.map(Into::into),
                    default_model,
                )?,
            };
            println!("Global config: {}", path.display());
        }
        CliPlan::Auth(args) => {
            let root = args
                .root
                .as_deref()
                .ok_or_else(|| io::Error::other("auth root is required"))?;
            let result = match args.action {
                zg_cli::AuthAction::Grant { .. } => zg_engine::authorization::grant(
                    root,
                    args.embedding.as_deref(),
                    args.endpoint.as_deref(),
                )?,
                zg_cli::AuthAction::Status => zg_engine::authorization::status(root)?,
                zg_cli::AuthAction::Revoke => zg_engine::authorization::revoke(root)?,
            };
            println!("{result}");
        }
        CliPlan::Server(plan) => match plan {
            ServerPlan::On(args) => {
                let config = server_config(args)?;
                show_server(&zg_daemon::start_server(&std::env::current_exe()?, &config).await?);
            }
            ServerPlan::Off(args) => {
                let home = zg_daemon::resolve_home(args.home)?;
                show_server(
                    &zg_daemon::stop_server_with_token(
                        &home,
                        zg_daemon::default_stop_timeout(),
                        args.token_file.as_deref(),
                    )
                    .await?,
                );
            }
            ServerPlan::Status(args) => {
                let home = zg_daemon::resolve_home(args.home)?;
                let s = zg_daemon::server_status(&home).await?;
                show_server(&s);
                if args.check_ready && !s.ready {
                    return Err(io::Error::other("server is not ready").into());
                }
            }
            ServerPlan::Run(args) => {
                zg_daemon::run_server(server_config(args)?, Arc::new(engine.clone())).await?;
            }
            ServerPlan::Stdio(args) => {
                zg_daemon::run_stdio_bridge(&std::env::current_exe()?, &server_config(args)?)
                    .await?;
            }
        },
        CliPlan::Install(args) => {
            zg_cli::execute_install(&args)?;
        }
        CliPlan::Uninstall(args) => {
            zg_cli::execute_uninstall(&args)?;
        }
    }
    engine.close();
    Ok(())
}

// The protocol suite scripts native CLI behavior without a shell or interpreter.
// This seam is enabled only by its explicit test-state environment variable.
async fn fake_run() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = std::env::args().skip(1).collect::<Vec<_>>();
    if args.first().is_some_and(|s| s == "--ki-cli") {
        args.remove(0);
    }
    crate::search::log(json!({"event":"cli","args":args.join(" ")}));
    match args.first().map(String::as_str).unwrap_or("") {
        "status" => {
            println!("Workspace index is ready");
            println!("  roots  {}", args.get(1).map(String::as_str).unwrap_or(""));
        }
        "server" => println!("daemon ready"),
        "query" => {
            println!("freshness: fresh");
            println!("1  {}", args.get(1).map(String::as_str).unwrap_or("query"));
        }
        "index" => {
            let state = std::env::var("KI_ZVEC_GREP_TEST_STATE")
                .ok()
                .and_then(|p| std::fs::read_to_string(p).ok())
                .and_then(|s| serde_json::from_str::<Value>(&s).ok())
                .unwrap_or(json!({}));
            if let Some(error) = state["indexFailure"].as_str() {
                return Err(io::Error::other(error.to_owned()).into());
            }
            if std::env::var("KI_ZVEC_GREP_FAKE_INDEX_FAIL").as_deref() == Ok("1") {
                return Err(io::Error::other("Error: Embedding model does not match the existing index.\nRe-run with --rebuild to change the embedding model.").into());
            }
            let seconds = std::env::var("KI_ZVEC_GREP_FAKE_INDEX_SLEEP")
                .ok()
                .and_then(|s| s.parse::<f64>().ok())
                .unwrap_or(0.3);
            tokio::time::sleep(Duration::from_secs_f64(seconds.max(0.0))).await;
            println!("Scanning files...");
            println!("Indexing files: 1/2");
            println!("Indexing complete");
            println!(
                "files\t2 scanned, 2 added, 0 modified, 0 retried, 0 unchanged, 0 deleted, 0 failed"
            );
            println!("entities\t2");
            println!("duration\t1s (1000ms)");
        }
        _ => {}
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn progress_keeps_model_counters_and_failed_files() {
        assert_eq!(
            progress_line("Scanning files...").unwrap()["phase"],
            "scanning"
        );
        assert_eq!(
            progress_line("Downloading local/potion · 1/2").unwrap()["model"],
            "local/potion"
        );
        let progress = progress_line("Indexing files: 3/8 (2 failed)").unwrap();
        assert_eq!(progress["done"], 3);
        assert_eq!(progress["total"], 8);
        assert_eq!(progress["failed"], 2);
        assert!(progress_line("arbitrary text").is_none());
    }
    #[test]
    fn legacy_cli_management_subcommands_translate_to_native_flags() {
        assert_eq!(
            legacy_args(vec![
                "--ki-cli".into(),
                "index".into(),
                "/root".into(),
                "--rebuild".into()
            ]),
            vec![
                OsString::from("zg"),
                "--index".into(),
                "/root".into(),
                "--rebuild".into()
            ]
        );
        assert_eq!(
            legacy_args(vec!["query".into(), "--fts".into(), "needle".into()]),
            vec![OsString::from("zg"), "--fts".into(), "needle".into()]
        );
    }
}
