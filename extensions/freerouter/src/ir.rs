use serde_json::{json, Map, Value};

/// Convert ki loop.Request IR into OpenAI-compatible chat messages + tools body fields.
pub fn ki_request_to_chat(request: &Value) -> (Vec<Value>, Option<Vec<Value>>, Option<u64>) {
    let messages = to_openrouter_messages(request);
    let tools = request
        .get("tools")
        .and_then(|t| t.as_array())
        .filter(|a| !a.is_empty())
        .map(|arr| {
            arr.iter()
                .map(|t| {
                    json!({
                        "type": "function",
                        "function": {
                            "name": t.get("name").and_then(|v| v.as_str()).unwrap_or(""),
                            "description": t.get("description").cloned().unwrap_or(json!("")),
                            "parameters": t.get("parameters").cloned().unwrap_or(json!({})),
                        }
                    })
                })
                .collect()
        });
    let max_tokens = request
        .get("maxTokens")
        .and_then(|v| v.as_u64())
        .filter(|&n| n > 0);
    (messages, tools, max_tokens)
}

pub fn to_openrouter_messages(request: &Value) -> Vec<Value> {
    let mut out = Vec::new();
    if let Some(system) = request.get("system").and_then(|v| v.as_str()) {
        if !system.is_empty() {
            out.push(json!({"role": "system", "content": system}));
        }
    }
    let Some(messages) = request.get("messages").and_then(|v| v.as_array()) else {
        return out;
    };
    for m in messages {
        let role = m.get("role").and_then(|v| v.as_str()).unwrap_or("user");
        if role == "toolResult" {
            let text = message_text(m);
            let text = if text.is_empty() {
                if content_has_image(m) {
                    "(see attached image)".to_string()
                } else {
                    String::new()
                }
            } else {
                text
            };
            out.push(json!({
                "role": "tool",
                "tool_call_id": m.get("toolCallId").and_then(|v| v.as_str()).unwrap_or(""),
                "content": text,
            }));
            continue;
        }
        if role == "assistant" {
            let mut entry = Map::new();
            entry.insert("role".into(), json!("assistant"));
            let mut text = String::new();
            let mut calls = Vec::new();
            if let Some(content) = m.get("content").and_then(|v| v.as_array()) {
                for c in content {
                    let ty = c.get("type").and_then(|v| v.as_str()).unwrap_or("");
                    if ty == "text" || ty.is_empty() {
                        if let Some(t) = c.get("text").and_then(|v| v.as_str()) {
                            text.push_str(t);
                        }
                    } else if ty == "thinking" {
                        if let Some(t) = c.get("thinking").and_then(|v| v.as_str()) {
                            if !t.is_empty() {
                                entry.insert("reasoning_content".into(), json!(t));
                            }
                        }
                    } else if ty == "toolCall" {
                        let args = valid_object_arguments_raw(
                            c.get("argumentsRaw").and_then(|v| v.as_str()),
                        )
                        .unwrap_or_else(|| {
                            c.get("arguments")
                                .cloned()
                                .unwrap_or(json!({}))
                                .to_string()
                        });
                        calls.push(json!({
                            "id": c.get("id").cloned().unwrap_or(json!("")),
                            "type": "function",
                            "function": {
                                "name": c.get("name").cloned().unwrap_or(json!("")),
                                "arguments": args,
                            }
                        }));
                    }
                }
            }
            if !text.is_empty() {
                entry.insert("content".into(), json!(text));
            }
            if !calls.is_empty() {
                entry.insert("tool_calls".into(), Value::Array(calls));
            }
            out.push(Value::Object(entry));
            continue;
        }
        // user
        let mut parts = Vec::new();
        let mut has_media = false;
        if let Some(content) = m.get("content").and_then(|v| v.as_array()) {
            for c in content {
                let ty = c.get("type").and_then(|v| v.as_str()).unwrap_or("");
                if ty == "image" {
                    if let Some(data) = c.get("data").and_then(|v| v.as_str()) {
                        has_media = true;
                        let mime = c
                            .get("mimeType")
                            .and_then(|v| v.as_str())
                            .unwrap_or("image/png");
                        parts.push(json!({
                            "type": "image_url",
                            "image_url": {"url": format!("data:{mime};base64,{data}")}
                        }));
                    }
                } else if (ty == "text" || ty.is_empty()) && c.get("text").is_some() {
                    if let Some(t) = c.get("text").and_then(|v| v.as_str()) {
                        if !t.is_empty() {
                            parts.push(json!({"type": "text", "text": t}));
                        }
                    }
                }
            }
        }
        if has_media {
            out.push(json!({"role": "user", "content": parts}));
        } else {
            out.push(json!({"role": "user", "content": message_text(m)}));
        }
    }
    out
}

fn message_text(message: &Value) -> String {
    let Some(content) = message.get("content").and_then(|v| v.as_array()) else {
        return String::new();
    };
    content
        .iter()
        .filter(|c| {
            let ty = c.get("type").and_then(|v| v.as_str()).unwrap_or("");
            ty == "text" || ty.is_empty()
        })
        .filter_map(|c| c.get("text").and_then(|v| v.as_str()))
        .collect::<Vec<_>>()
        .join("")
}

fn content_has_image(message: &Value) -> bool {
    message
        .get("content")
        .and_then(|v| v.as_array())
        .map(|arr| {
            arr.iter()
                .any(|c| c.get("type").and_then(|v| v.as_str()) == Some("image"))
        })
        .unwrap_or(false)
}

fn valid_object_arguments_raw(raw: Option<&str>) -> Option<String> {
    let raw = raw.filter(|s| !s.is_empty())?;
    let parsed: Value = serde_json::from_str(raw).ok()?;
    if parsed.is_object() {
        Some(raw.to_string())
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn converts_basic_ir() {
        let req = json!({
            "system": "sys",
            "messages": [
                {"role": "user", "content": [{"type": "text", "text": "hi"}]},
                {"role": "assistant", "content": [
                    {"type": "text", "text": "ok"},
                    {"type": "toolCall", "id": "1", "name": "Bash", "arguments": {"command": "ls"}}
                ]},
                {"role": "toolResult", "toolCallId": "1", "content": [{"type": "text", "text": "out"}]}
            ],
            "tools": [{"name": "Bash", "description": "d", "parameters": {"type": "object"}}],
            "maxTokens": 100
        });
        let (messages, tools, max_tokens) = ki_request_to_chat(&req);
        assert_eq!(messages[0]["role"], "system");
        assert_eq!(messages[1]["content"], "hi");
        assert!(messages[2].get("tool_calls").is_some());
        assert_eq!(messages[3]["role"], "tool");
        assert!(tools.unwrap().len() == 1);
        assert_eq!(max_tokens, Some(100));
    }
}
