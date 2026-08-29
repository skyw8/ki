use crate::event::StreamEvent;
use crate::race::{run_race, Pool, RaceInput};
use axum::extract::State;
use axum::http::{HeaderMap, HeaderValue, StatusCode};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use futures::stream::Stream;
use serde::Deserialize;
use serde_json::{json, Value};
use std::convert::Infallible;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::sync::mpsc;
use tokio_stream::wrappers::UnboundedReceiverStream;
use tokio_util::sync::CancellationToken;
use tower_http::cors::CorsLayer;

#[derive(Clone)]
pub struct HttpState {
    pub pool: Arc<Pool>,
}

pub fn router(state: HttpState) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/v1/models", get(list_models))
        .route("/v1/chat/completions", post(chat_completions))
        .layer(CorsLayer::permissive())
        .with_state(state)
}

async fn healthz() -> impl IntoResponse {
    Json(json!({"ok": true}))
}

async fn list_models(State(state): State<HttpState>) -> impl IntoResponse {
    let mut data = vec![json!({
        "id": "auto",
        "object": "model",
        "owned_by": "freerouter",
    })];
    if let Some(models) = state.pool.discovery.cached().await {
        for m in models {
            data.push(json!({
                "id": m.id,
                "object": "model",
                "owned_by": "openrouter-free",
                "name": m.name,
            }));
        }
    }
    Json(json!({"object": "list", "data": data}))
}

#[derive(Debug, Deserialize)]
struct ChatRequest {
    #[serde(default)]
    model: Option<String>,
    messages: Vec<Value>,
    #[serde(default)]
    tools: Option<Vec<Value>>,
    #[serde(default)]
    max_tokens: Option<u64>,
    #[serde(default)]
    stream: Option<bool>,
}

async fn chat_completions(
    State(state): State<HttpState>,
    headers: HeaderMap,
    Json(body): Json<ChatRequest>,
) -> Response {
    let api_key = state.pool.resolve_key(None).await;
    // Optional inbound bearer ignored for auth; freerouter uses its own OpenRouter key.
    let _ = headers.get(axum::http::header::AUTHORIZATION);

    if api_key.is_empty() {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({
                "error": {
                    "message": "No OpenRouter API key configured",
                    "type": "authentication_error"
                }
            })),
        )
            .into_response();
    }

    let model = body.model.unwrap_or_else(|| "auto".into());
    let pinned = if model.is_empty()
        || model == "auto"
        || model == "free-router/auto"
        || model == "freerouter/auto"
    {
        None
    } else if model.contains(":free") {
        Some(model.clone())
    } else {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "error": {
                    "message": format!("Unknown model '{model}'. Use 'auto' or a :free model id."),
                    "type": "invalid_request_error"
                }
            })),
        )
            .into_response();
    };

    let input = RaceInput {
        messages: body.messages,
        tools: body.tools,
        max_tokens: body.max_tokens,
        pinned_model: pinned,
    };
    let stream = body.stream.unwrap_or(false);
    let cancel = CancellationToken::new();

    if stream {
        let (tx, rx) = mpsc::unbounded_channel::<Result<Event, Infallible>>();
        let pool = Arc::clone(&state.pool);
        let cancel2 = cancel.clone();
        tokio::spawn(async move {
            let emit_tx = tx.clone();
            run_race(
                &pool,
                input,
                &api_key,
                cancel2,
                move |ev| {
                    forward_sse(&emit_tx, ev);
                },
                false,
            )
            .await;
            let _ = tx.send(Ok(Event::default().data("[DONE]")));
        });

        let stream = UnboundedReceiverStream::new(rx);
        let mut response = Sse::new(stream)
            .keep_alive(KeepAlive::default())
            .into_response();
        response.headers_mut().insert(
            "Cache-Control",
            HeaderValue::from_static("no-cache"),
        );
        response
    } else {
        let (tx, mut rx) = mpsc::unbounded_channel::<StreamEvent>();
        let pool = Arc::clone(&state.pool);
        let cancel2 = cancel.clone();
        let join = tokio::spawn(async move {
            run_race(
                &pool,
                input,
                &api_key,
                cancel2,
                move |ev| {
                    let _ = tx.send(ev);
                },
                false,
            )
            .await;
        });
        let mut final_message = None;
        let mut error = None;
        while let Some(ev) = rx.recv().await {
            match ev {
                StreamEvent::Done { message, .. } => final_message = Some(message),
                StreamEvent::Error { error: e, reason } => {
                    if reason == "aborted" {
                        error = Some((StatusCode::BAD_REQUEST, e));
                    } else if e.contains("exhausted") {
                        error = Some((StatusCode::SERVICE_UNAVAILABLE, e));
                    } else {
                        error = Some((StatusCode::BAD_GATEWAY, e));
                    }
                }
                _ => {}
            }
        }
        let _ = join.await;
        if let Some((code, msg)) = error {
            let typ = if code == StatusCode::SERVICE_UNAVAILABLE {
                "freerouter_exhausted"
            } else {
                "freerouter_error"
            };
            return (
                code,
                Json(json!({"error": {"message": msg, "type": typ}})),
            )
                .into_response();
        }
        let Some(message) = final_message else {
            return (
                StatusCode::BAD_GATEWAY,
                Json(json!({"error": {"message": "empty response", "type": "freerouter_error"}})),
            )
                .into_response();
        };
        let model_id = message
            .get("model")
            .and_then(|v| v.as_str())
            .unwrap_or("auto")
            .to_string();
        let completion = assistant_to_chat_completion(&message, &model_id);
        let mut response = Json(completion).into_response();
        if let Ok(v) = HeaderValue::from_str(&model_id) {
            response.headers_mut().insert("X-Freerouter-Model", v);
        }
        response
    }
}

fn forward_sse(tx: &mpsc::UnboundedSender<Result<Event, Infallible>>, ev: StreamEvent) {
    match ev {
        StreamEvent::TextDelta { delta, .. } => {
            let chunk = chat_chunk(None, Some(json!({"content": delta})), None);
            let _ = tx.send(Ok(Event::default().data(chunk.to_string())));
        }
        StreamEvent::ToolcallStart {
            tool_call_id,
            tool_name,
            ..
        } => {
            let chunk = chat_chunk(
                None,
                Some(json!({
                    "tool_calls": [{
                        "index": 0,
                        "id": tool_call_id,
                        "type": "function",
                        "function": {"name": tool_name, "arguments": ""}
                    }]
                })),
                None,
            );
            let _ = tx.send(Ok(Event::default().data(chunk.to_string())));
        }
        StreamEvent::ToolcallDelta { delta, .. } => {
            let chunk = chat_chunk(
                None,
                Some(json!({
                    "tool_calls": [{
                        "index": 0,
                        "function": {"arguments": delta}
                    }]
                })),
                None,
            );
            let _ = tx.send(Ok(Event::default().data(chunk.to_string())));
        }
        StreamEvent::Done { message, .. } => {
            let model = message
                .get("model")
                .and_then(|v| v.as_str())
                .unwrap_or("auto");
            let finish = match message.get("stopReason").and_then(|v| v.as_str()) {
                Some("toolUse") => "tool_calls",
                Some("length") => "length",
                _ => "stop",
            };
            let chunk = chat_chunk(Some(model), Some(json!({})), Some(finish));
            let _ = tx.send(Ok(Event::default().data(chunk.to_string())));
        }
        StreamEvent::Error { error, .. } => {
            let chunk = json!({
                "error": {"message": error, "type": "freerouter_error"}
            });
            let _ = tx.send(Ok(Event::default().data(chunk.to_string())));
        }
        _ => {}
    }
}

fn chat_chunk(model: Option<&str>, delta: Option<Value>, finish: Option<&str>) -> Value {
    json!({
        "id": format!("chatcmpl-fr-{}", now_ms()),
        "object": "chat.completion.chunk",
        "created": now_ms() / 1000,
        "model": model.unwrap_or("auto"),
        "choices": [{
            "index": 0,
            "delta": delta.unwrap_or(json!({})),
            "finish_reason": finish,
        }]
    })
}

fn assistant_to_chat_completion(message: &Value, model_id: &str) -> Value {
    let mut content_text = String::new();
    let mut tool_calls = Vec::new();
    if let Some(arr) = message.get("content").and_then(|v| v.as_array()) {
        for c in arr {
            match c.get("type").and_then(|v| v.as_str()) {
                Some("text") => {
                    if let Some(t) = c.get("text").and_then(|v| v.as_str()) {
                        content_text.push_str(t);
                    }
                }
                Some("toolCall") => {
                    tool_calls.push(json!({
                        "id": c.get("id").cloned().unwrap_or(json!("")),
                        "type": "function",
                        "function": {
                            "name": c.get("name").cloned().unwrap_or(json!("")),
                            "arguments": c.get("arguments").cloned().unwrap_or(json!({})).to_string(),
                        }
                    }));
                }
                _ => {}
            }
        }
    }
    let finish = match message.get("stopReason").and_then(|v| v.as_str()) {
        Some("toolUse") => "tool_calls",
        Some("length") => "length",
        _ => "stop",
    };
    let mut msg = json!({"role": "assistant"});
    if !tool_calls.is_empty() {
        msg["tool_calls"] = Value::Array(tool_calls);
        if !content_text.is_empty() {
            msg["content"] = json!(content_text);
        } else {
            msg["content"] = Value::Null;
        }
    } else {
        msg["content"] = json!(content_text);
    }
    let usage = message.get("usage").cloned().unwrap_or(json!({}));
    json!({
        "id": format!("chatcmpl-fr-{}", now_ms()),
        "object": "chat.completion",
        "created": now_ms() / 1000,
        "model": model_id,
        "choices": [{
            "index": 0,
            "message": msg,
            "finish_reason": finish,
        }],
        "usage": {
            "prompt_tokens": usage.get("input").cloned().unwrap_or(json!(0)),
            "completion_tokens": usage.get("output").cloned().unwrap_or(json!(0)),
            "total_tokens": usage.get("totalTokens").cloned().unwrap_or(json!(0)),
        }
    })
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

pub async fn serve(pool: Arc<Pool>, listen: &str) -> Result<(), String> {
    let state = HttpState { pool };
    let app = router(state);
    let listener = tokio::net::TcpListener::bind(listen)
        .await
        .map_err(|e| format!("failed to bind {listen}: {e} (set --listen / FREEROUTER_LISTEN / config listen)"))?;
    let addr = listener
        .local_addr()
        .map_err(|e| format!("local_addr: {e}"))?;
    eprintln!("[freerouter] HTTP listening on http://{addr}");
    axum::serve(listener, app)
        .await
        .map_err(|e| format!("HTTP server error: {e}"))
}

// silence unused import in some feature sets
#[allow(dead_code)]
fn _stream_type_check<S: Stream<Item = Result<Event, Infallible>>>(_: S) {}
