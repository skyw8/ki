use serde::{Deserialize, Serialize};
use serde_json::Value;

fn message_has_output(message: &Value) -> bool {
    let Some(arr) = message.get("content").and_then(|v| v.as_array()) else {
        return false;
    };
    arr.iter().any(|c| match c.get("type").and_then(|v| v.as_str()) {
        Some("text") => c
            .get("text")
            .and_then(|v| v.as_str())
            .map(|s| !s.is_empty())
            .unwrap_or(false),
        Some("toolCall") => true,
        _ => false,
    })
}

/// Compact stream events shared by OpenRouter parser, racer, sidecar, and HTTP.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum StreamEvent {
    Start,
    ThinkingStart {
        #[serde(rename = "contentIndex")]
        content_index: usize,
    },
    ThinkingDelta {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        delta: String,
    },
    ThinkingEnd {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        #[serde(default)]
        content: String,
    },
    TextStart {
        #[serde(rename = "contentIndex")]
        content_index: usize,
    },
    TextDelta {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        delta: String,
    },
    TextEnd {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        #[serde(default)]
        content: String,
    },
    ToolcallStart {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        #[serde(rename = "toolCallId")]
        tool_call_id: String,
        #[serde(rename = "toolName")]
        tool_name: String,
        #[serde(rename = "toolCall")]
        tool_call: Value,
    },
    ToolcallDelta {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        delta: String,
        #[serde(rename = "toolCallId")]
        tool_call_id: String,
    },
    ToolcallEnd {
        #[serde(rename = "contentIndex")]
        content_index: usize,
        #[serde(rename = "toolCallId")]
        tool_call_id: String,
        #[serde(rename = "toolName")]
        tool_name: String,
        #[serde(rename = "toolCall")]
        tool_call: Value,
    },
    Done {
        #[serde(skip_serializing_if = "Option::is_none")]
        reason: Option<String>,
        message: Value,
    },
    Error {
        reason: String,
        error: String,
    },
}

impl StreamEvent {
    pub fn is_qualifying(&self) -> bool {
        match self {
            StreamEvent::TextStart { .. } | StreamEvent::ToolcallStart { .. } => true,
            // Why: some free models finish with empty content (reasoning-only /
            // length) — treating that Done as a win yields a blank reply.
            StreamEvent::Done { message, .. } => message_has_output(message),
            _ => false,
        }
    }

    pub fn remap_index(self, offset: usize) -> Self {
        match self {
            StreamEvent::ThinkingStart { content_index } => StreamEvent::ThinkingStart {
                content_index: content_index + offset,
            },
            StreamEvent::ThinkingDelta {
                content_index,
                delta,
            } => StreamEvent::ThinkingDelta {
                content_index: content_index + offset,
                delta,
            },
            StreamEvent::ThinkingEnd {
                content_index,
                content,
            } => StreamEvent::ThinkingEnd {
                content_index: content_index + offset,
                content,
            },
            StreamEvent::TextStart { content_index } => StreamEvent::TextStart {
                content_index: content_index + offset,
            },
            StreamEvent::TextDelta {
                content_index,
                delta,
            } => StreamEvent::TextDelta {
                content_index: content_index + offset,
                delta,
            },
            StreamEvent::TextEnd {
                content_index,
                content,
            } => StreamEvent::TextEnd {
                content_index: content_index + offset,
                content,
            },
            StreamEvent::ToolcallStart {
                content_index,
                tool_call_id,
                tool_name,
                tool_call,
            } => StreamEvent::ToolcallStart {
                content_index: content_index + offset,
                tool_call_id,
                tool_name,
                tool_call,
            },
            StreamEvent::ToolcallDelta {
                content_index,
                delta,
                tool_call_id,
            } => StreamEvent::ToolcallDelta {
                content_index: content_index + offset,
                delta,
                tool_call_id,
            },
            StreamEvent::ToolcallEnd {
                content_index,
                tool_call_id,
                tool_name,
                tool_call,
            } => StreamEvent::ToolcallEnd {
                content_index: content_index + offset,
                tool_call_id,
                tool_name,
                tool_call,
            },
            other => other,
        }
    }

    /// Serialize for ki provider.stream.event (flat type field, not internally tagged enum issues).
    pub fn to_sidecar_params(&self, request_id: &str) -> Value {
        let mut v = serde_json::to_value(self).unwrap_or(Value::Null);
        if let Value::Object(ref mut map) = v {
            map.insert("requestId".into(), Value::String(request_id.to_string()));
        }
        v
    }
}
