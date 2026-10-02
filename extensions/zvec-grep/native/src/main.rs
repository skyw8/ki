mod cli;
mod commands;
mod config;
mod render;
mod rpc;
mod search;

use config::{Config, string};
use rpc::{Host, Rpc};
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex, atomic::AtomicUsize},
};
use tokio::{
    io::{AsyncBufReadExt, BufReader},
    sync::{Mutex as AsyncMutex, Semaphore},
    task::JoinSet,
};
use tokio_util::sync::CancellationToken;
use zg_engine::ZvecGrep;

const SEARCH_CONCURRENCY: usize = 4;
const SIDECAR_WORKERS: usize = SEARCH_CONCURRENCY + 1;

fn runtime(cli: bool) -> tokio::runtime::Runtime {
    let mut builder = tokio::runtime::Builder::new_multi_thread();
    if !cli {
        // Reserve a control worker alongside the four admitted native searches,
        // whose async polls can include synchronous storage work. Tokio's CPU-count default
        // eagerly starts dozens of idle async workers on large hosts. Native model
        // compute and blocking I/O use separate pools; leave those and CLI/index
        // parallelism unchanged.
        let workers = std::thread::available_parallelism()
            .map(usize::from)
            .unwrap_or(1)
            .min(SIDECAR_WORKERS);
        builder.worker_threads(workers);
    }
    builder
        .enable_all()
        .build()
        .expect("create zvec-grep runtime")
}

pub struct PendingSearch {
    pub id: Value,
    pub root: String,
    pub signal: CancellationToken,
}
pub struct App {
    pub rpc: Arc<Rpc>,
    pub sessions: Mutex<HashMap<String, String>>,
    pub active: Mutex<HashMap<String, PendingSearch>>,
    pub config: Mutex<Option<Config>>,
    pub engine: Mutex<Option<(String, ZvecGrep)>>,
    pub slots: Semaphore,
    pub gates: Mutex<HashMap<String, std::sync::Weak<AsyncMutex<()>>>>,
    pub jobs: Mutex<HashMap<String, commands::Job>>,
    pub status_timers: Mutex<HashMap<String, tokio::task::AbortHandle>>,
    pub fake_locks: AtomicUsize,
    pub fake_calls: AtomicUsize,
}
impl App {
    pub fn config(&self) -> Result<Config, String> {
        let mut cache = self.config.lock().unwrap();
        if cache.is_none() {
            *cache = Some(Config::load()?);
        }
        Ok(cache.clone().unwrap())
    }
    pub fn cwd(&self, session: &str) -> String {
        self.sessions
            .lock()
            .unwrap()
            .get(session)
            .cloned()
            .unwrap_or_else(|| {
                std::env::current_dir()
                    .unwrap()
                    .to_string_lossy()
                    .into_owned()
            })
    }
    pub fn host(&self, session: &str) -> Host {
        Host {
            rpc: self.rpc.clone(),
            session: session.into(),
        }
    }
    pub fn dispose(&self) {
        if let Some((_, engine)) = self.engine.lock().unwrap().take() {
            engine.close();
            search::log(json!({"event":"close","options":{}}));
        }
    }
    pub fn job_active(&self, root: &str) -> bool {
        let root = commands::clean(std::path::PathBuf::from(root))
            .to_string_lossy()
            .into_owned();
        self.jobs
            .lock()
            .unwrap()
            .values()
            .any(|job| job.root == root)
    }
    async fn handle(
        self: Arc<Self>,
        method: &str,
        params: Value,
        id: Value,
    ) -> Result<Value, String> {
        match method {
            "initialize" => Ok(
                json!({"tools":[serde_json::from_str::<Value>(include_str!("../search-tool.json")).unwrap()],"commands":commands::specs(),"subscriptions":[]}),
            ),
            "session.open" => {
                let session = string(&params["sessionId"]);
                let cwd = if string(&params["cwd"]).is_empty() {
                    std::env::current_dir()
                        .unwrap()
                        .to_string_lossy()
                        .into_owned()
                } else {
                    string(&params["cwd"]).into()
                };
                if !session.is_empty() {
                    self.sessions
                        .lock()
                        .unwrap()
                        .insert(session.into(), cwd.clone());
                }
                Ok(json!({"root":cwd}))
            }
            "session.close" => {
                self.sessions
                    .lock()
                    .unwrap()
                    .remove(string(&params["sessionId"]));
                Ok(json!({}))
            }
            "config.updated" => {
                *self.config.lock().unwrap() = None;
                self.dispose();
                Ok(json!({}))
            }
            "shutdown" => {
                self.dispose();
                Ok(json!({}))
            }
            "tool.execute" => {
                if params["name"] != "zvec_grep_search" {
                    return Ok(search::error_result(&format!(
                        "unknown tool {}",
                        string(&params["name"])
                    )));
                }
                let cwd = self.cwd(string(&params["sessionId"]));
                if self.rpc.is_closed() {
                    return Ok(search::cancelled(&cwd));
                }
                let key = Rpc::key(&id);
                let signal = CancellationToken::new();
                if !id.is_null() {
                    self.active.lock().unwrap().insert(
                        key.clone(),
                        PendingSearch {
                            id: id.clone(),
                            root: cwd.clone(),
                            signal: signal.clone(),
                        },
                    );
                }
                let result = search::execute(
                    self.clone(),
                    params["args"].clone(),
                    cwd,
                    signal,
                    key.clone(),
                )
                .await;
                self.active.lock().unwrap().remove(&key);
                result
            }
            "cancel" => {
                let key = Rpc::key(&params["id"]);
                if let Some(pending) = self.active.lock().unwrap().get(&key) {
                    pending.signal.cancel();
                    self.rpc
                        .cancel_reply(pending.id.clone(), search::cancelled(&pending.root));
                }
                Ok(json!({}))
            }
            "command.invoke" => commands::invoke(self, params).await,
            _ => Ok(json!({})),
        }
    }
}

fn main() {
    runtime(std::env::args_os().len() > 1).block_on(run());
}

async fn run() {
    if std::env::args_os().len() > 1 {
        if let Err(e) = cli::run().await {
            eprintln!("{e}");
            std::process::exit(1);
        }
        return;
    }
    let rpc = Rpc::new();
    let app = Arc::new(App {
        rpc: rpc.clone(),
        sessions: Mutex::new(HashMap::new()),
        active: Mutex::new(HashMap::new()),
        config: Mutex::new(None),
        engine: Mutex::new(None),
        slots: Semaphore::new(SEARCH_CONCURRENCY),
        gates: Mutex::new(HashMap::new()),
        jobs: Mutex::new(HashMap::new()),
        status_timers: Mutex::new(HashMap::new()),
        fake_locks: AtomicUsize::new(0),
        fake_calls: AtomicUsize::new(0),
    });
    let mut lines = BufReader::new(tokio::io::stdin()).lines();
    let mut handlers = JoinSet::new();
    while let Ok(Some(line)) = lines.next_line().await {
        let Ok(msg) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        if let Some(method) = msg["method"].as_str() {
            let method = method.to_owned();
            let app = app.clone();
            let rpc = rpc.clone();
            handlers.spawn(async move {
                let id = msg["id"].clone();
                let result = app.handle(&method, msg["params"].clone(), id.clone()).await;
                if !id.is_null() {
                    rpc.reply(id, result);
                } else if let Err(e) = result {
                    eprintln!("zvec-grep notification {method}: {e}");
                }
            });
        } else {
            rpc.receive(msg);
        }
        while handlers.try_join_next().is_some() {}
    }
    // EOF can arrive before a spawned handler or its queued stdout write runs.
    // Drain replies so a one-request pipe receives the same result as a live host,
    // but bound shutdown because the host may stop reading stdout or native work may stall.
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(2);
    rpc.close_input();
    for pending in app.active.lock().unwrap().values() {
        pending.signal.cancel();
    }
    let _ = tokio::time::timeout_at(deadline, async {
        while handlers.join_next().await.is_some() {}
    })
    .await;
    handlers.abort_all();
    let _ = tokio::time::timeout_at(deadline, rpc.flush()).await;
    let _ =
        tokio::time::timeout_at(deadline, tokio::task::spawn_blocking(move || app.dispose())).await;
    // Dropping Tokio waits for blocking stdout/native threads; process exit enforces the EOF deadline.
    std::process::exit(0);
}

#[cfg(test)]
mod runtime_tests {
    use super::*;

    #[test]
    fn sidecar_workers_reserve_control_capacity_without_scaling_with_host_cpus() {
        let runtime = runtime(false);
        let expected = std::thread::available_parallelism()
            .map(usize::from)
            .unwrap_or(1)
            .min(SIDECAR_WORKERS);
        assert_eq!(runtime.metrics().num_workers(), expected);
        assert_eq!(
            runtime.handle().runtime_flavor(),
            tokio::runtime::RuntimeFlavor::MultiThread
        );
    }

    #[test]
    fn cli_retains_tokio_worker_defaults() {
        let runtime = runtime(true);
        let expected = std::env::var("TOKIO_WORKER_THREADS")
            .ok()
            .map(|value| value.parse::<usize>().unwrap())
            .unwrap_or_else(|| {
                std::thread::available_parallelism()
                    .map(usize::from)
                    .unwrap_or(1)
            });
        assert_eq!(runtime.metrics().num_workers(), expected);
    }
}
