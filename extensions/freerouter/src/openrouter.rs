use crate::event::StreamEvent;
use futures::StreamExt;
use serde_json::{json, Value};
use std::collections::HashMap;
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

#[derive(Debug)]
pub enum ModelError {
    Exhausted { model_id: String, status: u16 },
    Fatal(String),
    Other(String),
    Cancelled,
}

impl std::fmt::Display for ModelError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ModelError::Exhausted { model_id, status } => {
                write!(f, "Model {model_id} quota exceeded (HTTP {status})")
            }
            ModelError::Fatal(m) | ModelError::Other(m) => write!(f, "{m}"),
            ModelError::Cancelled => write!(f, "cancelled"),
        }
    }
}

pub fn build_chat_body(
    model_id: &str,
    messages: &[Value],
    tools: &Option<Vec<Value>>,
    max_tokens: Option<u64>,
) -> Value {
    let mut body = json!({
        "model": model_id,
        "stream": true,
        "messages": messages,
    });
    if let Some(mt) = max_tokens {
        if mt > 0 {
            body["max_tokens"] = json!(mt);
        }
    }
    if let Some(t) = tools {
        if !t.is_empty() {
            body["tools"] = Value::Array(t.clone());
        }
    }
    body
}

fn normalize_stop_reason(finish: &str) -> &'static str {
    match finish {
        "tool_calls" => "toolUse",
        "length" => "length",
        _ => "stop",
    }
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn new_assistant_message(model_id: &str) -> Value {
    json!({
        "role": "assistant",
        "api": "freerouter",
        "provider": "free-router",
        "model": model_id,
        "content": [],
        "usage": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0},
        "stopReason": "stop",
        "timestamp": now_ms(),
    })
}

/// Stream one free model into `tx`. Returns the final assistant message or an error.
pub async fn stream_free_model(
    client: &reqwest::Client,
    model_id: &str,
    messages: &[Value],
    tools: &Option<Vec<Value>>,
    max_tokens: Option<u64>,
    api_key: &str,
    base_url: &str,
    cancel: CancellationToken,
    tx: mpsc::UnboundedSender<StreamEvent>,
) -> Result<Value, ModelError> {
    let url = format!("{}/chat/completions", base_url.trim_end_matches('/'));
    let body = build_chat_body(model_id, messages, tools, max_tokens);

    let request = client
        .post(&url)
        .header("Authorization", format!("Bearer {api_key}"))
        .header("Content-Type", "application/json")
        .header("X-Title", "freerouter")
        .json(&body);

    let response = tokio::select! {
        _ = cancel.cancelled() => {
            let _ = tx.send(StreamEvent::Error {
                reason: "aborted".into(),
                error: format!("{model_id} aborted"),
            });
            return Err(ModelError::Cancelled);
        }
        res = request.send() => res.map_err(|e| {
            if cancel.is_cancelled() {
                ModelError::Cancelled
            } else {
                ModelError::Other(e.to_string())
            }
        })?,
    };

    let status = response.status().as_u16();
    if status == 402 {
        let _ = tx.send(StreamEvent::Error {
            reason: "error".into(),
            error: "OpenRouter API key has insufficient credits.".into(),
        });
        return Err(ModelError::Fatal(
            "OpenRouter API key has insufficient credits. Add credits at openrouter.ai/credits."
                .into(),
        ));
    }
    if status == 429 || status >= 500 || status == 400 || status == 422 {
        let _ = tx.send(StreamEvent::Error {
            reason: "error".into(),
            error: format!("{model_id} failed (HTTP {status})"),
        });
        return Err(ModelError::Exhausted {
            model_id: model_id.to_string(),
            status,
        });
    }
    if !response.status().is_success() {
        let message = format!(
            "OpenRouter error: {status} {}",
            response.status().canonical_reason().unwrap_or("")
        );
        let _ = tx.send(StreamEvent::Error {
            reason: "error".into(),
            error: message.clone(),
        });
        return Err(ModelError::Other(message));
    }

    let mut output = new_assistant_message(model_id);
    let _ = tx.send(StreamEvent::Start);

    let mut text_started = false;
    let mut pending: HashMap<u64, PendingTool> = HashMap::new();
    let mut buffer = String::new();

    let mut stream = response.bytes_stream();
    loop {
        let chunk = tokio::select! {
            _ = cancel.cancelled() => {
                close_open_blocks(&mut output, &mut text_started, &mut pending, &tx);
                let _ = tx.send(StreamEvent::Error {
                    reason: "aborted".into(),
                    error: format!("{model_id} aborted"),
                });
                return Err(ModelError::Cancelled);
            }
            item = stream.next() => item,
        };
        let Some(item) = chunk else { break };
        let bytes = item.map_err(|e| {
            close_open_blocks(&mut output, &mut text_started, &mut pending, &tx);
            ModelError::Other(e.to_string())
        })?;
        buffer.push_str(&String::from_utf8_lossy(&bytes));
        while let Some(pos) = buffer.find('\n') {
            let line = buffer[..pos].trim_end_matches('\r').to_string();
            buffer = buffer[pos + 1..].to_string();
            if !line.starts_with("data: ") {
                continue;
            }
            let data = line[6..].trim();
            if data == "[DONE]" {
                finish(&mut output, &mut text_started, &mut pending, &tx);
                return Ok(output);
            }
            let Ok(chunk) = serde_json::from_str::<Value>(data) else {
                continue;
            };
            if let Err(e) = handle_chunk(
                &chunk,
                model_id,
                &mut output,
                &mut text_started,
                &mut pending,
                &tx,
            ) {
                close_open_blocks(&mut output, &mut text_started, &mut pending, &tx);
                let _ = tx.send(StreamEvent::Error {
                    reason: "error".into(),
                    error: e.to_string(),
                });
                return Err(e);
            }
        }
    }
    // no [DONE]
    if buffer.starts_with("data: ") {
        let data = buffer[6..].trim();
        if data != "[DONE]" {
            if let Ok(chunk) = serde_json::from_str::<Value>(data) {
                let _ = handle_chunk(
                    &chunk,
                    model_id,
                    &mut output,
                    &mut text_started,
                    &mut pending,
                    &tx,
                );
            }
        }
    }
    finish(&mut output, &mut text_started, &mut pending, &tx);
    Ok(output)
}

struct PendingTool {
    content_index: usize,
    id: String,
    name: String,
    args_buffer: String,
}

fn handle_chunk(
    chunk: &Value,
    model_id: &str,
    output: &mut Value,
    text_started: &mut bool,
    pending: &mut HashMap<u64, PendingTool>,
    tx: &mpsc::UnboundedSender<StreamEvent>,
) -> Result<(), ModelError> {
    if let Some(err) = chunk.get("error") {
        let code = err
            .get("code")
            .or_else(|| err.get("status"))
            .and_then(|v| v.as_u64())
            .unwrap_or(0) as u16;
        if code == 402 {
            return Err(ModelError::Fatal(
                err.get("message")
                    .and_then(|v| v.as_str())
                    .unwrap_or("insufficient credits")
                    .to_string(),
            ));
        }
        return Err(ModelError::Exhausted {
            model_id: model_id.to_string(),
            status: if code == 0 { 400 } else { code },
        });
    }
    let choice = chunk.pointer("/choices/0");
    let delta = choice.and_then(|c| c.get("delta"));
    if let Some(usage) = chunk.get("usage") {
        output["usage"]["input"] = json!(usage.get("prompt_tokens").and_then(|v| v.as_u64()).unwrap_or(0));
        output["usage"]["output"] =
            json!(usage.get("completion_tokens").and_then(|v| v.as_u64()).unwrap_or(0));
        output["usage"]["totalTokens"] =
            json!(usage.get("total_tokens").and_then(|v| v.as_u64()).unwrap_or(0));
    }
    if let Some(fr) = choice
        .and_then(|c| c.get("finish_reason"))
        .and_then(|v| v.as_str())
    {
        output["stopReason"] = json!(normalize_stop_reason(fr));
    }

    // Why: some free models emit empty content:"" chunks (or reasoning-only
    // tokens) before real text. Treating "" as text_start would win the race
    // with an empty stream and look like a successful but blank reply.
    if let Some(content) = delta
        .and_then(|d| d.get("content"))
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
    {
        if !*text_started {
            let arr = output["content"].as_array_mut().unwrap();
            arr.push(json!({"type": "text", "text": ""}));
            let _ = tx.send(StreamEvent::TextStart { content_index: 0 });
            *text_started = true;
        }
        if let Some(block) = output["content"].as_array_mut().and_then(|a| a.get_mut(0)) {
            let existing = block.get("text").and_then(|v| v.as_str()).unwrap_or("");
            block["text"] = json!(format!("{existing}{content}"));
        }
        let _ = tx.send(StreamEvent::TextDelta {
            content_index: 0,
            delta: content.to_string(),
        });
    }

    if let Some(tool_calls) = delta.and_then(|d| d.get("tool_calls")).and_then(|v| v.as_array()) {
        for tc in tool_calls {
            let tc_idx = tc.get("index").and_then(|v| v.as_u64()).unwrap_or(0);
            if let Some(id) = tc.get("id").and_then(|v| v.as_str()) {
                let content_index = output["content"].as_array().map(|a| a.len()).unwrap_or(0);
                let name = tc
                    .pointer("/function/name")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                let args0 = tc
                    .pointer("/function/arguments")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                output["content"]
                    .as_array_mut()
                    .unwrap()
                    .push(json!({"type": "toolCall", "id": id, "name": name, "arguments": {}}));
                pending.insert(
                    tc_idx,
                    PendingTool {
                        content_index,
                        id: id.to_string(),
                        name: name.clone(),
                        args_buffer: args0,
                    },
                );
                let _ = tx.send(StreamEvent::ToolcallStart {
                    content_index,
                    tool_call_id: id.to_string(),
                    tool_name: name.clone(),
                    tool_call: json!({"type": "toolCall", "id": id, "name": name}),
                });
            } else if let Some(args) = tc.pointer("/function/arguments").and_then(|v| v.as_str()) {
                if let Some(p) = pending.get_mut(&tc_idx) {
                    p.args_buffer.push_str(args);
                    let _ = tx.send(StreamEvent::ToolcallDelta {
                        content_index: p.content_index,
                        delta: args.to_string(),
                        tool_call_id: p.id.clone(),
                    });
                }
            }
        }
    }
    Ok(())
}

fn close_open_blocks(
    output: &mut Value,
    text_started: &mut bool,
    pending: &mut HashMap<u64, PendingTool>,
    tx: &mpsc::UnboundedSender<StreamEvent>,
) {
    if *text_started {
        let text = output["content"]
            .as_array()
            .and_then(|a| a.first())
            .and_then(|b| b.get("text"))
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let _ = tx.send(StreamEvent::TextEnd {
            content_index: 0,
            content: text,
        });
        *text_started = false;
    }
    for (_, p) in pending.drain() {
        let args: Value = serde_json::from_str(&p.args_buffer)
            .unwrap_or_else(|_| json!({"_raw": p.args_buffer}));
        if let Some(block) = output["content"]
            .as_array_mut()
            .and_then(|a| a.get_mut(p.content_index))
        {
            if block.get("type").and_then(|v| v.as_str()) == Some("toolCall") {
                block["arguments"] = args.clone();
            }
        }
        let _ = tx.send(StreamEvent::ToolcallEnd {
            content_index: p.content_index,
            tool_call_id: p.id.clone(),
            tool_name: p.name.clone(),
            tool_call: json!({"type": "toolCall", "id": p.id, "name": p.name, "arguments": args}),
        });
    }
}

fn finish(
    output: &mut Value,
    text_started: &mut bool,
    pending: &mut HashMap<u64, PendingTool>,
    tx: &mpsc::UnboundedSender<StreamEvent>,
) {
    close_open_blocks(output, text_started, pending, tx);
    let reason = output
        .get("stopReason")
        .and_then(|v| v.as_str())
        .unwrap_or("stop")
        .to_string();
    let _ = tx.send(StreamEvent::Done {
        reason: Some(reason),
        message: output.clone(),
    });
}
