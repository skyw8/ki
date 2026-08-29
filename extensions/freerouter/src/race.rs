use crate::config::Config;
use crate::discovery::{ModelDiscovery, ModelInfo};
use crate::event::StreamEvent;
use crate::openrouter::{stream_free_model, ModelError};
use crate::router::FreeRouter;
use serde_json::{json, Value};
use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{mpsc, Mutex, RwLock};
use tokio_util::sync::CancellationToken;

pub struct Pool {
    pub config: Arc<RwLock<Config>>,
    pub discovery: Arc<ModelDiscovery>,
    pub router: Mutex<Option<FreeRouter>>,
    pub client: reqwest::Client,
    /// Last API key seen on a ki provider.stream (host credential). HTTP
    /// requests reuse it so the always-on proxy works when only the provider
    /// credential is configured (no config.json / env key).
    pub last_api_key: Mutex<String>,
}

impl Pool {
    pub fn new(config: Config, client: reqwest::Client) -> Arc<Self> {
        let config = Arc::new(RwLock::new(config));
        let discovery = Arc::new(ModelDiscovery::new(client.clone()));
        discovery.spawn_background_refresh(Arc::clone(&config));
        Arc::new(Self {
            config,
            discovery,
            router: Mutex::new(None),
            client,
            last_api_key: Mutex::new(String::new()),
        })
    }

    pub async fn ensure(&self, api_key: &str) -> Result<Vec<ModelInfo>, String> {
        let base_url = self.config.read().await.base_url.clone();
        let models = self.discovery.ensure(api_key, &base_url).await?;
        let cfg = self.config.read().await.clone();
        let mut guard = self.router.lock().await;
        match guard.as_mut() {
            Some(r) => r.set_models(models.iter().map(|m| m.id.clone()).collect()),
            None => {
                *guard = Some(FreeRouter::new(
                    models.iter().map(|m| m.id.clone()).collect(),
                    cfg.exhausted_ttl_ms,
                    cfg.slow_ttl_ms,
                ));
            }
        }
        Ok(models)
    }

    pub async fn rebuild_router_from_cache(&self) {
        let cfg = self.config.read().await.clone();
        if let Some(models) = self.discovery.cached().await {
            let mut guard = self.router.lock().await;
            *guard = Some(FreeRouter::new(
                models.iter().map(|m| m.id.clone()).collect(),
                cfg.exhausted_ttl_ms,
                cfg.slow_ttl_ms,
            ));
        }
    }

    pub async fn resolve_key(&self, credential: Option<&str>) -> String {
        let cfg = self.config.read().await;
        let key = crate::config::resolve_api_key(&cfg.api_key, credential);
        if !key.is_empty() {
            *self.last_api_key.lock().await = key.clone();
            return key;
        }
        self.last_api_key.lock().await.clone()
    }

}

fn batch_timeout_ms(config: &Config, batch: usize) -> u64 {
    let factors = [1.0_f64, 1.5, 2.0];
    let f = factors[(batch.saturating_sub(1)).min(factors.len() - 1)];
    (config.first_token_timeout_ms as f64 * f).round() as u64
}

pub struct RaceInput {
    pub messages: Vec<Value>,
    pub tools: Option<Vec<Value>>,
    pub max_tokens: Option<u64>,
    pub pinned_model: Option<String>,
}

pub async fn run_race(
    pool: &Pool,
    input: RaceInput,
    api_key: &str,
    cancel: CancellationToken,
    emit: impl Fn(StreamEvent) + Send + Sync,
    for_sidecar: bool,
) {
    if api_key.is_empty() {
        emit(StreamEvent::Error {
            reason: "error".into(),
            error: "No OpenRouter API key configured. Set it via PUT /v1/providers/free-router/credential, the extension config apiKey, or OPENROUTER_API_KEY.".into(),
        });
        return;
    }

    let models = match pool.ensure(api_key).await {
        Ok(v) => v,
        Err(err) => {
            emit(StreamEvent::Error {
                reason: "error".into(),
                error: format!("freerouter: {err}"),
            });
            return;
        }
    };

    if let Some(model_id) = input.pinned_model.clone() {
        run_single(pool, &input, api_key, &model_id, cancel, &emit, for_sidecar).await;
        return;
    }

    let offset = if for_sidecar { 1 } else { 0 };
    let thinking = Arc::new(Mutex::new(String::new()));

    if for_sidecar {
        emit(StreamEvent::ThinkingStart { content_index: 0 });
        {
            let mut t = thinking.lock().await;
            t.push_str("Searching free models...");
        }
        emit(StreamEvent::ThinkingDelta {
            content_index: 0,
            delta: "Searching free models...".into(),
        });
    }

    let config = pool.config.read().await.clone();
    let mut tried = HashSet::new();

    for batch in 1..=config.max_batches {
        if cancel.is_cancelled() {
            emit(StreamEvent::Error {
                reason: "aborted".into(),
                error: "Request was cancelled.".into(),
            });
            return;
        }

        let candidates = {
            let mut router = pool.router.lock().await;
            let router = router.as_mut().unwrap();
            router
                .next_models(models.len())
                .into_iter()
                .filter(|id| !tried.contains(id))
                .take(config.race_width)
                .collect::<Vec<_>>()
        };
        if candidates.is_empty() {
            break;
        }
        for id in &candidates {
            tried.insert(id.clone());
        }

        eprintln!("[freerouter] racing: {}", candidates.join(", "));
        if for_sidecar {
            let delta = format!("\nRound {batch}: {}", candidates.join(", "));
            thinking.lock().await.push_str(&delta);
            emit(StreamEvent::ThinkingDelta {
                content_index: 0,
                delta,
            });
        }

        let outcome = race_batch(
            pool,
            &input,
            api_key,
            &candidates,
            batch,
            cancel.clone(),
            &emit,
            offset,
            for_sidecar,
            &thinking,
        )
        .await;

        {
            let mut router = pool.router.lock().await;
            let router = router.as_mut().unwrap();
            for id in &outcome.exhausted {
                router.mark_exhausted(id);
            }
        }

        if outcome.cancelled {
            emit(StreamEvent::Error {
                reason: "aborted".into(),
                error: "Request was cancelled.".into(),
            });
            return;
        }
        if let Some(msg) = outcome.fatal {
            if for_sidecar {
                let delta = format!("\n{msg}");
                thinking.lock().await.push_str(&delta);
                emit(StreamEvent::ThinkingDelta {
                    content_index: 0,
                    delta,
                });
            }
            emit(StreamEvent::Error {
                reason: "error".into(),
                error: msg,
            });
            return;
        }
        if outcome.winner.is_some() {
            return;
        }
        if outcome.stalled || outcome.winner_stream_error {
            return;
        }

        let mut router = pool.router.lock().await;
        let router = router.as_mut().unwrap();
        if outcome.timed_out {
            for id in &candidates {
                router.mark_slow(id);
            }
        } else {
            for id in &candidates {
                router.mark_exhausted(id);
            }
        }
    }

    let message =
        "All free models exhausted. They will recover automatically — please try again in a moment.";
    if for_sidecar {
        let delta = format!("\n{message}");
        thinking.lock().await.push_str(&delta);
        emit(StreamEvent::ThinkingDelta {
            content_index: 0,
            delta,
        });
    }
    emit(StreamEvent::Error {
        reason: "error".into(),
        error: message.into(),
    });
}

async fn run_single(
    pool: &Pool,
    input: &RaceInput,
    api_key: &str,
    model_id: &str,
    cancel: CancellationToken,
    emit: &impl Fn(StreamEvent),
    for_sidecar: bool,
) {
    let (tx, mut rx) = mpsc::unbounded_channel();
    let client = pool.client.clone();
    let base_url = pool.config.read().await.base_url.clone();
    let messages = input.messages.clone();
    let tools = input.tools.clone();
    let max_tokens = input.max_tokens;
    let model = model_id.to_string();
    let cancel2 = cancel.clone();
    let key = api_key.to_string();
    tokio::spawn(async move {
        let _ = stream_free_model(
            &client,
            &model,
            &messages,
            &tools,
            max_tokens,
            &key,
            &base_url,
            cancel2,
            tx,
        )
        .await;
    });

    let offset = if for_sidecar { 1 } else { 0 };
    let thinking = if for_sidecar {
        emit(StreamEvent::ThinkingStart { content_index: 0 });
        let delta = format!("Using {model_id}");
        emit(StreamEvent::ThinkingDelta {
            content_index: 0,
            delta: delta.clone(),
        });
        Some(delta)
    } else {
        None
    };

    while let Some(ev) = rx.recv().await {
        match ev {
            StreamEvent::Start => {}
            StreamEvent::Done { reason, message } => {
                let message = if let Some(t) = &thinking {
                    prepend_thinking(message, t)
                } else {
                    message
                };
                emit(StreamEvent::Done { reason, message });
            }
            other => emit(other.remap_index(offset)),
        }
    }
}

struct BatchOutcome {
    winner: Option<String>,
    exhausted: Vec<String>,
    fatal: Option<String>,
    timed_out: bool,
    cancelled: bool,
    stalled: bool,
    winner_stream_error: bool,
}

async fn race_batch(
    pool: &Pool,
    input: &RaceInput,
    api_key: &str,
    candidates: &[String],
    batch: usize,
    cancel: CancellationToken,
    emit: &impl Fn(StreamEvent),
    offset: usize,
    for_sidecar: bool,
    thinking: &Arc<Mutex<String>>,
) -> BatchOutcome {
    let config = pool.config.read().await.clone();
    let base_url = config.base_url.clone();
    let n = candidates.len();
    let mut child_cancels = Vec::with_capacity(n);
    let exhausted = Arc::new(Mutex::new(Vec::new()));
    let fatal = Arc::new(Mutex::new(None::<String>));
    let (merged_tx, mut merged_rx) = mpsc::unbounded_channel::<(usize, StreamEvent)>();

    for (idx, model_id) in candidates.iter().enumerate() {
        let (tx, mut rx) = mpsc::unbounded_channel();
        let child = cancel.child_token();
        let client = pool.client.clone();
        let messages = input.messages.clone();
        let tools = input.tools.clone();
        let max_tokens = input.max_tokens;
        let api_key = api_key.to_string();
        let base_url = base_url.clone();
        let model_id = model_id.clone();
        let exhausted2 = Arc::clone(&exhausted);
        let fatal2 = Arc::clone(&fatal);
        let child2 = child.clone();
        let merged_tx = merged_tx.clone();

        tokio::spawn(async move {
            match stream_free_model(
                &client,
                &model_id,
                &messages,
                &tools,
                max_tokens,
                &api_key,
                &base_url,
                child2,
                tx,
            )
            .await
            {
                Err(ModelError::Exhausted { model_id, .. }) => {
                    exhausted2.lock().await.push(model_id);
                }
                Err(ModelError::Fatal(msg)) => {
                    let mut f = fatal2.lock().await;
                    if f.is_none() {
                        *f = Some(msg);
                    }
                }
                _ => {}
            }
        });

        tokio::spawn(async move {
            while let Some(ev) = rx.recv().await {
                if merged_tx.send((idx, ev)).is_err() {
                    break;
                }
            }
        });
        child_cancels.push(child);
    }
    drop(merged_tx);

    let mut buffers: Vec<Vec<StreamEvent>> = (0..n).map(|_| Vec::new()).collect();
    let deadline = Duration::from_millis(batch_timeout_ms(&config, batch));
    let deadline_at = tokio::time::Instant::now() + deadline;
    let mut active: HashSet<usize> = (0..n).collect();

    let abort_all = |child_cancels: &[CancellationToken]| {
        for c in child_cancels {
            c.cancel();
        }
    };

    loop {
        if cancel.is_cancelled() {
            abort_all(&child_cancels);
            return BatchOutcome {
                winner: None,
                exhausted: exhausted.lock().await.clone(),
                fatal: fatal.lock().await.clone(),
                timed_out: false,
                cancelled: true,
                stalled: false,
                winner_stream_error: false,
            };
        }
        if fatal.lock().await.is_some() {
            abort_all(&child_cancels);
            return BatchOutcome {
                winner: None,
                exhausted: exhausted.lock().await.clone(),
                fatal: fatal.lock().await.clone(),
                timed_out: false,
                cancelled: false,
                stalled: false,
                winner_stream_error: false,
            };
        }
        if active.is_empty() {
            return BatchOutcome {
                winner: None,
                exhausted: exhausted.lock().await.clone(),
                fatal: fatal.lock().await.clone(),
                timed_out: false,
                cancelled: false,
                stalled: false,
                winner_stream_error: false,
            };
        }

        let sleep = tokio::time::sleep_until(deadline_at);
        tokio::pin!(sleep);
        let result = tokio::select! {
            _ = &mut sleep => None,
            _ = cancel.cancelled() => {
                abort_all(&child_cancels);
                return BatchOutcome {
                    winner: None,
                    exhausted: exhausted.lock().await.clone(),
                    fatal: fatal.lock().await.clone(),
                    timed_out: false,
                    cancelled: true,
                    stalled: false,
                    winner_stream_error: false,
                };
            }
            msg = merged_rx.recv() => msg,
        };

        let Some((idx, event)) = result else {
            abort_all(&child_cancels);
            return BatchOutcome {
                winner: None,
                exhausted: exhausted.lock().await.clone(),
                fatal: fatal.lock().await.clone(),
                timed_out: true,
                cancelled: false,
                stalled: false,
                winner_stream_error: false,
            };
        };
        if !active.contains(&idx) {
            continue;
        }

        if event.is_qualifying() {
            for (j, c) in child_cancels.iter().enumerate() {
                if j != idx {
                    c.cancel();
                }
            }
            for e in buffers[idx].drain(..) {
                if !matches!(e, StreamEvent::Start) {
                    emit(e.remap_index(offset));
                }
            }
            if for_sidecar {
                let delta = format!("\nUsing {}", candidates[idx]);
                thinking.lock().await.push_str(&delta);
                emit(StreamEvent::ThinkingDelta {
                    content_index: 0,
                    delta,
                });
            }

            match event {
                StreamEvent::Done { reason, message } => {
                    let message = if for_sidecar {
                        let t = thinking.lock().await.clone();
                        prepend_thinking(message, t.trim())
                    } else {
                        message
                    };
                    emit(StreamEvent::Done { reason, message });
                    return BatchOutcome {
                        winner: Some(candidates[idx].clone()),
                        exhausted: exhausted.lock().await.clone(),
                        fatal: fatal.lock().await.clone(),
                        timed_out: false,
                        cancelled: false,
                        stalled: false,
                        winner_stream_error: false,
                    };
                }
                qual => emit(qual.remap_index(offset)),
            }

            // Forward remaining winner events from the merged bus (filter by idx).
            let idle = Duration::from_millis(config.idle_timeout_ms);
            loop {
                let idle_sleep = tokio::time::sleep(idle);
                tokio::pin!(idle_sleep);
                let next = tokio::select! {
                    _ = &mut idle_sleep => None,
                    _ = cancel.cancelled() => {
                        child_cancels[idx].cancel();
                        return BatchOutcome {
                            winner: Some(candidates[idx].clone()),
                            exhausted: exhausted.lock().await.clone(),
                            fatal: fatal.lock().await.clone(),
                            timed_out: false,
                            cancelled: true,
                            stalled: false,
                            winner_stream_error: false,
                        };
                    }
                    msg = merged_rx.recv() => msg,
                };
                let Some((i, ev)) = next else {
                    child_cancels[idx].cancel();
                    eprintln!(
                        "[freerouter] winner {} stalled after winning; closing stream",
                        candidates[idx]
                    );
                    emit(StreamEvent::Error {
                        reason: "error".into(),
                        error: format!("{} stream stalled", candidates[idx]),
                    });
                    return BatchOutcome {
                        winner: Some(candidates[idx].clone()),
                        exhausted: exhausted.lock().await.clone(),
                        fatal: fatal.lock().await.clone(),
                        timed_out: false,
                        cancelled: false,
                        stalled: true,
                        winner_stream_error: false,
                    };
                };
                if i != idx {
                    continue;
                }
                match ev {
                    StreamEvent::Start => {}
                    StreamEvent::Done { reason, message } => {
                        let message = if for_sidecar {
                            let t = thinking.lock().await.clone();
                            prepend_thinking(message, t.trim())
                        } else {
                            message
                        };
                        emit(StreamEvent::Done { reason, message });
                        return BatchOutcome {
                            winner: Some(candidates[idx].clone()),
                            exhausted: exhausted.lock().await.clone(),
                            fatal: fatal.lock().await.clone(),
                            timed_out: false,
                            cancelled: false,
                            stalled: false,
                            winner_stream_error: false,
                        };
                    }
                    StreamEvent::Error { reason, error } => {
                        emit(StreamEvent::Error { reason, error }.remap_index(offset));
                        return BatchOutcome {
                            winner: Some(candidates[idx].clone()),
                            exhausted: exhausted.lock().await.clone(),
                            fatal: fatal.lock().await.clone(),
                            timed_out: false,
                            cancelled: false,
                            stalled: false,
                            winner_stream_error: true,
                        };
                    }
                    other => emit(other.remap_index(offset)),
                }
            }
        }

        if matches!(event, StreamEvent::Error { .. }) {
            active.remove(&idx);
            continue;
        }
        buffers[idx].push(event);
    }
}

fn prepend_thinking(mut message: Value, thinking: &str) -> Value {
    let block = json!({"type": "thinking", "thinking": thinking});
    match message.get_mut("content") {
        Some(Value::Array(arr)) => arr.insert(0, block),
        _ => {
            message["content"] = json!([block]);
        }
    }
    message
}
