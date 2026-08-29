use serde::Deserialize;
use std::env;
use std::fs;
use std::path::Path;

pub const DEFAULT_LISTEN: &str = "127.0.0.1:18427";
pub const DEFAULT_BASE_URL: &str = "https://openrouter.ai/api/v1";

#[derive(Debug, Clone)]
pub struct Config {
    pub api_key: String,
    pub base_url: String,
    pub listen: String,
    pub race_width: usize,
    pub max_batches: usize,
    pub exhausted_ttl_ms: u64,
    pub slow_ttl_ms: u64,
    pub first_token_timeout_ms: u64,
    pub idle_timeout_ms: u64,
    pub refresh_interval_ms: u64,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            api_key: String::new(),
            base_url: DEFAULT_BASE_URL.to_string(),
            listen: DEFAULT_LISTEN.to_string(),
            race_width: 2,
            max_batches: 3,
            exhausted_ttl_ms: 90_000,
            slow_ttl_ms: 15_000,
            first_token_timeout_ms: 10_000,
            idle_timeout_ms: 30_000,
            refresh_interval_ms: 3_600_000,
        }
    }
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RawConfig {
    api_key: Option<String>,
    base_url: Option<String>,
    listen: Option<String>,
    race_width: Option<u64>,
    max_batches: Option<u64>,
    exhausted_ttl_ms: Option<u64>,
    slow_ttl_ms: Option<u64>,
    first_token_timeout_ms: Option<u64>,
    idle_timeout_ms: Option<u64>,
    refresh_interval_ms: Option<u64>,
}

fn clamp(n: u64, fallback: u64, min: u64, max: u64) -> u64 {
    if n == 0 && fallback != 0 {
        // treat missing/zero from partial overrides carefully — callers pass Some only when set
    }
    n.clamp(min, max)
}

fn clamp_or(v: Option<u64>, fallback: u64, min: u64, max: u64) -> u64 {
    match v {
        Some(n) if n > 0 => clamp(n, fallback, min, max),
        _ => fallback,
    }
}

fn env_api_key() -> String {
    env::var("OPENROUTER_API_KEY")
        .or_else(|_| env::var("FREEROUTER_API_KEY"))
        .unwrap_or_default()
        .trim()
        .to_string()
}

fn env_base_url() -> Option<String> {
    env::var("OPENROUTER_BASE_URL")
        .ok()
        .map(|s| s.trim().trim_end_matches('/').to_string())
        .filter(|s| !s.is_empty())
}

fn env_listen() -> Option<String> {
    env::var("FREEROUTER_LISTEN")
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

fn apply_env_overrides(cfg: &mut Config) {
    if let Ok(v) = env::var("FREEROUTER_RACE_WIDTH") {
        if let Ok(n) = v.parse::<u64>() {
            cfg.race_width = clamp_or(Some(n), cfg.race_width as u64, 1, 8) as usize;
        }
    }
    if let Ok(v) = env::var("FREEROUTER_MAX_BATCHES") {
        if let Ok(n) = v.parse::<u64>() {
            cfg.max_batches = clamp_or(Some(n), cfg.max_batches as u64, 1, 6) as usize;
        }
    }
    if let Some(url) = env_base_url() {
        cfg.base_url = url;
    }
    if let Some(listen) = env_listen() {
        cfg.listen = listen;
    }
    let key = env_api_key();
    if !key.is_empty() && cfg.api_key.is_empty() {
        cfg.api_key = key;
    }
}

/// Standalone: env + CLI overrides. No extension config.json.
pub fn load_standalone(listen_override: Option<String>, base_url_override: Option<String>) -> Config {
    let mut cfg = Config::default();
    // Env key is primary for standalone.
    cfg.api_key = env_api_key();
    apply_env_overrides(&mut cfg);
    if let Some(u) = base_url_override.filter(|s| !s.trim().is_empty()) {
        cfg.base_url = u.trim().trim_end_matches('/').to_string();
    }
    if let Some(l) = listen_override.filter(|s| !s.trim().is_empty()) {
        cfg.listen = l.trim().to_string();
    }
    cfg
}

/// Sidecar: extension config.json, then env fallbacks.
pub fn load_sidecar(extension_root: &Path) -> Config {
    let mut cfg = Config::default();
    let path = extension_root.join("config.json");
    if let Ok(text) = fs::read_to_string(&path) {
        if let Ok(raw) = serde_json::from_str::<RawConfig>(&text) {
            merge_raw(&mut cfg, raw);
        }
    }
    // Env fills empty key / overrides listen+base when set.
    let env_key = env_api_key();
    if cfg.api_key.is_empty() && !env_key.is_empty() {
        cfg.api_key = env_key;
    }
    if let Some(url) = env_base_url() {
        // Prefer explicit config baseUrl when non-empty; else env.
        if cfg.base_url == DEFAULT_BASE_URL || cfg.base_url.is_empty() {
            cfg.base_url = url;
        }
    }
    // FREEROUTER_LISTEN overrides file when set (ops escape hatch).
    if let Some(listen) = env_listen() {
        cfg.listen = listen;
    }
    if let Ok(v) = env::var("FREEROUTER_RACE_WIDTH") {
        if let Ok(n) = v.parse::<u64>() {
            cfg.race_width = clamp_or(Some(n), 2, 1, 8) as usize;
        }
    }
    cfg.base_url = cfg.base_url.trim().trim_end_matches('/').to_string();
    if cfg.base_url.is_empty() {
        cfg.base_url = DEFAULT_BASE_URL.to_string();
    }
    if cfg.listen.trim().is_empty() {
        cfg.listen = DEFAULT_LISTEN.to_string();
    }
    cfg
}

fn merge_raw(cfg: &mut Config, raw: RawConfig) {
    if let Some(k) = raw.api_key {
        if !k.is_empty() {
            cfg.api_key = k;
        }
    }
    if let Some(u) = raw.base_url {
        let u = u.trim().trim_end_matches('/').to_string();
        if !u.is_empty() {
            cfg.base_url = u;
        }
    }
    if let Some(l) = raw.listen {
        let l = l.trim().to_string();
        if !l.is_empty() {
            cfg.listen = l;
        }
    }
    cfg.race_width = clamp_or(raw.race_width, cfg.race_width as u64, 1, 8) as usize;
    cfg.max_batches = clamp_or(raw.max_batches, cfg.max_batches as u64, 1, 6) as usize;
    cfg.exhausted_ttl_ms = clamp_or(raw.exhausted_ttl_ms, cfg.exhausted_ttl_ms, 1_000, 30 * 60_000);
    cfg.slow_ttl_ms = clamp_or(raw.slow_ttl_ms, cfg.slow_ttl_ms, 1_000, 10 * 60_000);
    cfg.first_token_timeout_ms =
        clamp_or(raw.first_token_timeout_ms, cfg.first_token_timeout_ms, 1_000, 5 * 60_000);
    cfg.idle_timeout_ms = clamp_or(raw.idle_timeout_ms, cfg.idle_timeout_ms, 1_000, 10 * 60_000);
    cfg.refresh_interval_ms = clamp_or(
        raw.refresh_interval_ms,
        cfg.refresh_interval_ms,
        60_000,
        24 * 60 * 60_000,
    );
}

pub fn resolve_api_key(config_key: &str, credential_key: Option<&str>) -> String {
    if let Some(k) = credential_key.map(str::trim).filter(|s| !s.is_empty()) {
        return k.to_string();
    }
    let cfg = config_key.trim();
    if !cfg.is_empty() {
        return cfg.to_string();
    }
    env_api_key()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults() {
        let c = Config::default();
        assert_eq!(c.listen, DEFAULT_LISTEN);
        assert_eq!(c.race_width, 2);
    }
}
