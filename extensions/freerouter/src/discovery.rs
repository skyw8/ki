use crate::config::Config;
use serde::Deserialize;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{Mutex, Notify, RwLock};

const DISCOVERY_TIMEOUT: Duration = Duration::from_secs(10);
const DEFAULT_CONTEXT_WINDOW: u64 = 128_000;
const DEFAULT_MAX_TOKENS: u64 = 4_096;

const FAST_PROVIDER_PREFIXES: &[&str] = &[
    "groq/",
    "cerebras/",
    "fireworks/",
    "together/",
    "mistralai/",
];

const NON_ASSISTANT_MARKERS: &[&str] = &[
    "content-safety",
    "moderation",
    "guard",
    "-vl",
    "/vl",
    "vision",
];

#[derive(Debug, Clone)]
pub struct ModelInfo {
    pub id: String,
    pub name: String,
    pub context_length: u64,
    pub max_tokens: u64,
}

#[derive(Debug, Deserialize)]
struct ModelsResponse {
    data: Option<Vec<RawModel>>,
}

#[derive(Debug, Deserialize)]
struct RawModel {
    id: Option<String>,
    name: Option<String>,
    context_length: Option<u64>,
    top_provider: Option<TopProvider>,
}

#[derive(Debug, Deserialize)]
struct TopProvider {
    max_completion_tokens: Option<u64>,
}

fn speed_score(model_id: &str) -> usize {
    let lower = model_id.to_lowercase();
    FAST_PROVIDER_PREFIXES
        .iter()
        .position(|p| lower.starts_with(p))
        .unwrap_or(FAST_PROVIDER_PREFIXES.len())
}

fn is_general_assistant(model_id: &str) -> bool {
    let lower = model_id.to_lowercase();
    !NON_ASSISTANT_MARKERS.iter().any(|m| lower.contains(m))
}

pub async fn fetch_free_models(
    client: &reqwest::Client,
    api_key: &str,
    base_url: &str,
) -> Result<Vec<ModelInfo>, String> {
    if api_key.is_empty() {
        return Err("OpenRouter API key is required".into());
    }
    let url = format!("{}/models", base_url.trim_end_matches('/'));
    let response = client
        .get(&url)
        .header("Authorization", format!("Bearer {api_key}"))
        .timeout(DISCOVERY_TIMEOUT)
        .send()
        .await
        .map_err(|e| format!("Failed to fetch OpenRouter models: {e}"))?;
    if !response.status().is_success() {
        return Err(format!(
            "Failed to fetch OpenRouter models: {} {}",
            response.status().as_u16(),
            response.status().canonical_reason().unwrap_or("")
        ));
    }
    let payload: ModelsResponse = response
        .json()
        .await
        .map_err(|e| format!("Invalid models response: {e}"))?;
    let mut models: Vec<ModelInfo> = payload
        .data
        .unwrap_or_default()
        .into_iter()
        .filter_map(|m| {
            let id = m.id?;
            if !id.contains(":free") || !is_general_assistant(&id) {
                return None;
            }
            Some(ModelInfo {
                name: m.name.unwrap_or_else(|| id.clone()),
                context_length: m.context_length.unwrap_or(DEFAULT_CONTEXT_WINDOW),
                max_tokens: m
                    .top_provider
                    .and_then(|t| t.max_completion_tokens)
                    .unwrap_or(DEFAULT_MAX_TOKENS),
                id,
            })
        })
        .collect();
    models.sort_by(|a, b| {
        speed_score(&a.id)
            .cmp(&speed_score(&b.id))
            .then(a.context_length.cmp(&b.context_length))
    });
    if models.is_empty() {
        return Err("No free models found on OpenRouter".into());
    }
    Ok(models)
}

pub struct ModelDiscovery {
    models: RwLock<Option<Vec<ModelInfo>>>,
    inflight: Mutex<Option<Arc<Notify>>>,
    client: reqwest::Client,
}

impl ModelDiscovery {
    pub fn new(client: reqwest::Client) -> Self {
        Self {
            models: RwLock::new(None),
            inflight: Mutex::new(None),
            client,
        }
    }

    pub async fn cached(&self) -> Option<Vec<ModelInfo>> {
        self.models.read().await.clone()
    }

    pub async fn ensure(&self, api_key: &str, base_url: &str) -> Result<Vec<ModelInfo>, String> {
        if let Some(m) = self.models.read().await.clone() {
            if !m.is_empty() {
                return Ok(m);
            }
        }
        // Single-flight
        let notify = {
            let mut guard = self.inflight.lock().await;
            if let Some(n) = guard.as_ref() {
                let n = n.clone();
                drop(guard);
                n.notified().await;
                return self
                    .models
                    .read()
                    .await
                    .clone()
                    .filter(|m| !m.is_empty())
                    .ok_or_else(|| "No free models found on OpenRouter".to_string());
            }
            let n = Arc::new(Notify::new());
            *guard = Some(n.clone());
            n
        };
        let result = fetch_free_models(&self.client, api_key, base_url).await;
        match &result {
            Ok(models) => {
                *self.models.write().await = Some(models.clone());
            }
            Err(_) => {}
        }
        {
            let mut guard = self.inflight.lock().await;
            *guard = None;
        }
        notify.notify_waiters();
        result
    }

    pub async fn refresh(&self, api_key: &str, base_url: &str) -> Result<(), String> {
        let models = fetch_free_models(&self.client, api_key, base_url).await?;
        if !models.is_empty() {
            *self.models.write().await = Some(models);
        }
        Ok(())
    }

    pub fn spawn_background_refresh(
        self: &Arc<Self>,
        config: Arc<tokio::sync::RwLock<Config>>,
    ) {
        let discovery = Arc::clone(self);
        tokio::spawn(async move {
            loop {
                let (interval, api_key, base_url) = {
                    let c = config.read().await;
                    (
                        Duration::from_millis(c.refresh_interval_ms),
                        c.api_key.clone(),
                        c.base_url.clone(),
                    )
                };
                tokio::time::sleep(interval).await;
                let key = if api_key.is_empty() {
                    std::env::var("OPENROUTER_API_KEY")
                        .or_else(|_| std::env::var("FREEROUTER_API_KEY"))
                        .unwrap_or_default()
                } else {
                    api_key
                };
                if key.is_empty() {
                    continue;
                }
                if let Err(err) = discovery.refresh(&key, &base_url).await {
                    eprintln!("[freerouter] model list refresh failed: {err}");
                }
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filters_and_sorts() {
        let raw = vec![
            ModelInfo {
                id: "deepseek/deepseek-v3:free".into(),
                name: "DeepSeek".into(),
                context_length: 64000,
                max_tokens: 8192,
            },
            ModelInfo {
                id: "groq/llama:free".into(),
                name: "Llama".into(),
                context_length: 32000,
                max_tokens: 4096,
            },
            ModelInfo {
                id: "meta/vision:free".into(),
                name: "Vision".into(),
                context_length: 1000,
                max_tokens: 1000,
            },
            ModelInfo {
                id: "paid/model".into(),
                name: "Paid".into(),
                context_length: 1000,
                max_tokens: 1000,
            },
        ];
        let mut models: Vec<_> = raw
            .into_iter()
            .filter(|m| m.id.contains(":free") && is_general_assistant(&m.id))
            .collect();
        models.sort_by(|a, b| {
            speed_score(&a.id)
                .cmp(&speed_score(&b.id))
                .then(a.context_length.cmp(&b.context_length))
        });
        assert_eq!(
            models.iter().map(|m| m.id.as_str()).collect::<Vec<_>>(),
            vec!["groq/llama:free", "deepseek/deepseek-v3:free"]
        );
    }
}
