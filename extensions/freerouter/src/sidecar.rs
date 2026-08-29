use crate::config;
use crate::event::StreamEvent;
use crate::ir::ki_request_to_chat;
use crate::race::{run_race, Pool, RaceInput};
use serde_json::{json, Value};
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::sync::{mpsc, Mutex};
use tokio_util::sync::CancellationToken;

pub async fn run_sidecar(pool: Arc<Pool>, extension_root: PathBuf) -> Result<(), String> {
    let stdin = BufReader::new(tokio::io::stdin());
    let mut lines = stdin.lines();
    let stdout = Arc::new(Mutex::new(tokio::io::stdout()));
    let cancels: Arc<Mutex<HashMap<String, CancellationToken>>> =
        Arc::new(Mutex::new(HashMap::new()));

    while let Some(line) = lines
        .next_line()
        .await
        .map_err(|e| format!("stdin read: {e}"))?
    {
        if line.trim().is_empty() {
            continue;
        }
        let msg: Value = match serde_json::from_str(&line) {
            Ok(v) => v,
            Err(_) => continue,
        };
        if msg.get("jsonrpc").and_then(|v| v.as_str()) != Some("2.0") {
            continue;
        }
        if msg.get("method").is_none() {
            continue;
        }
        let method = msg.get("method").and_then(|v| v.as_str()).unwrap_or("");
        let id = msg.get("id").cloned();
        let params = msg.get("params").cloned().unwrap_or(json!({}));

        match method {
            "initialize" => {
                respond(
                    &stdout,
                    id,
                    json!({"tools": [], "commands": [], "fallback": false, "subscriptions": []}),
                )
                .await;
                let listen = pool.config.read().await.listen.clone();
                let _ = write_msg(
                    &stdout,
                    &json!({
                        "jsonrpc": "2.0",
                        "id": format!("freerouter-status-{}", std::process::id()),
                        "method": "ui.setGlobalStatus",
                        "params": {
                            // Why: chips without a custom status inherit runtime
                            // ready→success (green). An explicit info tone stays
                            // gray and looks "off" next to other ready extensions.
                            "text": "freerouter",
                            "tone": "success"
                        }
                    }),
                )
                .await;
            }
            "provider.stream.start" => {
                let request_id = params
                    .get("requestId")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                respond(&stdout, id, json!({"accepted": true})).await;

                let cancel = CancellationToken::new();
                cancels
                    .lock()
                    .await
                    .insert(request_id.clone(), cancel.clone());

                let pool = Arc::clone(&pool);
                let stdout = Arc::clone(&stdout);
                let cancels = Arc::clone(&cancels);
                tokio::spawn(async move {
                    let (ev_tx, mut ev_rx) = mpsc::unbounded_channel::<StreamEvent>();
                    let stdout_w = Arc::clone(&stdout);
                    let rid = request_id.clone();
                    let writer = tokio::spawn(async move {
                        while let Some(ev) = ev_rx.recv().await {
                            let params = ev.to_sidecar_params(&rid);
                            let _ = write_msg(
                                &stdout_w,
                                &json!({
                                    "jsonrpc": "2.0",
                                    "method": "provider.stream.event",
                                    "params": params
                                }),
                            )
                            .await;
                        }
                    });

                    let request_wrapper = params.get("request").cloned().unwrap_or(json!({}));
                    let credential_key = request_wrapper
                        .pointer("/credential/apiKey")
                        .and_then(|v| v.as_str());
                    let api_key = pool.resolve_key(credential_key).await;
                    let ki_req = request_wrapper
                        .get("request")
                        .cloned()
                        .unwrap_or_else(|| request_wrapper.clone());
                    let (messages, tools, max_tokens) = ki_request_to_chat(&ki_req);
                    let input = RaceInput {
                        messages,
                        tools,
                        max_tokens,
                        pinned_model: None,
                    };
                    run_race(
                        &pool,
                        input,
                        &api_key,
                        cancel,
                        move |ev| {
                            let _ = ev_tx.send(ev);
                        },
                        true,
                    )
                    .await;
                    let _ = writer.await;
                    cancels.lock().await.remove(&request_id);
                });
            }
            "provider.stream.cancel" => {
                let request_id = params
                    .get("requestId")
                    .and_then(|v| v.as_str())
                    .unwrap_or("");
                if let Some(c) = cancels.lock().await.remove(request_id) {
                    c.cancel();
                }
                if id.is_some() {
                    respond(&stdout, id, json!({})).await;
                }
            }
            "cancel" => {
                if id.is_some() {
                    respond(&stdout, id, json!({})).await;
                }
            }
            "config.updated" => {
                let cfg = config::load_sidecar(&extension_root);
                *pool.config.write().await = cfg;
                pool.rebuild_router_from_cache().await;
                if id.is_some() {
                    respond(&stdout, id, json!({})).await;
                }
            }
            "shutdown" => {
                if id.is_some() {
                    respond(&stdout, id, json!({})).await;
                }
                break;
            }
            _ => {
                if id.is_some() {
                    write_msg(
                        &stdout,
                        &json!({
                            "jsonrpc": "2.0",
                            "id": id,
                            "error": {"code": -32601, "message": format!("method not found: {method}")}
                        }),
                    )
                    .await?;
                }
            }
        }
    }
    Ok(())
}

async fn respond(stdout: &Arc<Mutex<tokio::io::Stdout>>, id: Option<Value>, result: Value) {
    let Some(id) = id else { return };
    let _ = write_msg(
        stdout,
        &json!({"jsonrpc": "2.0", "id": id, "result": result}),
    )
    .await;
}

async fn write_msg(
    stdout: &Arc<Mutex<tokio::io::Stdout>>,
    msg: &Value,
) -> Result<(), String> {
    let mut line = serde_json::to_string(msg).map_err(|e| e.to_string())?;
    line.push('\n');
    let mut out = stdout.lock().await;
    out.write_all(line.as_bytes())
        .await
        .map_err(|e| e.to_string())?;
    out.flush().await.map_err(|e| e.to_string())?;
    Ok(())
}

pub fn extension_root_from_env() -> PathBuf {
    std::env::var("KI_EXTENSION_ROOT")
        .map(PathBuf::from)
        .unwrap_or_else(|_| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")))
}
