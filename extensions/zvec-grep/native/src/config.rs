use serde_json::Value;
use std::path::PathBuf;

#[derive(Clone, Debug)]
pub struct Config {
    pub embedding: String,
    pub device: String,
    pub mode: String,
    pub max_results: u64,
    pub ignored_globs: Vec<String>,
    pub timeout_ms: u64,
    pub notify: bool,
}
impl Default for Config {
    fn default() -> Self {
        Self {
            embedding: "local/potion-code-16m-v2".into(),
            device: "auto".into(),
            mode: "in-process".into(),
            max_results: 5,
            ignored_globs: vec![],
            timeout_ms: 90_000,
            notify: false,
        }
    }
}
pub fn root() -> PathBuf {
    std::env::var_os("KI_EXTENSION_ROOT")
        .map(PathBuf::from)
        .unwrap_or_else(|| std::env::current_dir().unwrap())
}
pub fn string(value: &Value) -> &str {
    value.as_str().unwrap_or("")
}
pub fn trim(value: &str) -> &str {
    // ECMAScript trims BOM and excludes NEL; Rust's Unicode whitespace set differs.
    value.trim_matches(|c| {
        matches!(c, '\u{0009}'..='\u{000d}' | '\u{0020}' | '\u{00a0}' | '\u{1680}' | '\u{2000}'..='\u{200a}' | '\u{2028}' | '\u{2029}' | '\u{202f}' | '\u{205f}' | '\u{3000}' | '\u{feff}')
    })
}
fn js_string(value: &Value) -> String {
    match value {
        Value::String(s) => s.clone(),
        Value::Null => "null".into(),
        Value::Bool(b) => b.to_string(),
        Value::Number(n) => n.as_f64().unwrap().to_string(),
        Value::Array(items) => items
            .iter()
            .map(|v| {
                if v.is_null() {
                    String::new()
                } else {
                    js_string(v)
                }
            })
            .collect::<Vec<_>>()
            .join(","),
        Value::Object(_) => "[object Object]".into(),
    }
}
pub fn strings(value: &Value) -> Vec<String> {
    let items = match value {
        Value::String(_) => vec![value],
        Value::Array(items) => items.iter().collect(),
        _ => vec![],
    };
    items
        .into_iter()
        .filter_map(|v| {
            let s = trim(string(v));
            (!s.is_empty()).then(|| s.to_owned())
        })
        .collect()
}
pub fn number(value: &Value) -> Option<f64> {
    match value {
        Value::Number(n) => n.as_f64(),
        Value::String(s) => string_number(s),
        Value::Bool(b) => Some(if *b { 1.0 } else { 0.0 }),
        Value::Null => Some(0.0),
        // Number(array) first applies Array.toString, including nested arrays and empty null entries.
        Value::Array(_) => string_number(&js_string(value)),
        Value::Object(_) => None,
    }
}
fn string_number(value: &str) -> Option<f64> {
    let value = trim(value);
    if value.is_empty() {
        return Some(0.0);
    }
    if matches!(value, "Infinity" | "+Infinity" | "-Infinity") {
        return Some(if value.starts_with('-') {
            f64::NEG_INFINITY
        } else {
            f64::INFINITY
        });
    }
    let radix = if value.starts_with("0x") || value.starts_with("0X") {
        Some(16)
    } else if value.starts_with("0b") || value.starts_with("0B") {
        Some(2)
    } else if value.starts_with("0o") || value.starts_with("0O") {
        Some(8)
    } else {
        None
    };
    if let Some(radix) = radix {
        let digits = &value[2..];
        if digits.is_empty() {
            return None;
        }
        return digits.chars().try_fold(0.0, |n, c| {
            c.to_digit(radix)
                .map(|digit| n * f64::from(radix) + f64::from(digit))
        });
    }
    // Rust accepts spellings such as "inf" that Number(string) does not; validate the decimal grammar first.
    let bytes = value.as_bytes();
    let mut cursor = usize::from(matches!(bytes[0], b'+' | b'-'));
    let mut digits = 0;
    while cursor < bytes.len() && bytes[cursor].is_ascii_digit() {
        cursor += 1;
        digits += 1;
    }
    if bytes.get(cursor) == Some(&b'.') {
        cursor += 1;
        while cursor < bytes.len() && bytes[cursor].is_ascii_digit() {
            cursor += 1;
            digits += 1;
        }
    }
    if digits == 0 {
        return None;
    }
    if matches!(bytes.get(cursor), Some(b'e' | b'E')) {
        cursor += 1;
        if matches!(bytes.get(cursor), Some(b'+' | b'-')) {
            cursor += 1;
        }
        let exponent_start = cursor;
        while cursor < bytes.len() && bytes[cursor].is_ascii_digit() {
            cursor += 1;
        }
        if exponent_start == cursor {
            return None;
        }
    }
    (cursor == bytes.len())
        .then(|| value.parse().ok())
        .flatten()
}
fn validate_version(raw: &Value) -> Result<(), String> {
    let Some(version) = raw.get("version").filter(|v| !v.is_null()) else {
        return Ok(());
    };
    let Some(version) = version.as_i64() else {
        return Err("zvec-grep config.json is invalid: version must be an integer".into());
    };
    if version > 1 {
        return Err("zvec-grep config.json has a newer state version".into());
    }
    if version < 0 {
        return Err("zvec-grep config.json is invalid: unsupported state version".into());
    }
    Ok(())
}
pub fn clamp(value: Option<&Value>, min: u64, max: u64, fallback: u64) -> u64 {
    value
        .and_then(number)
        .filter(|n| n.is_finite())
        .map(|n| n.floor().max(min as f64).min(max as f64) as u64)
        .unwrap_or(fallback)
}
impl Config {
    pub fn load() -> Result<Self, String> {
        let path = root().join("config.json");
        let raw = match std::fs::read_to_string(path) {
            Ok(s) => serde_json::from_str::<Value>(&s)
                .map_err(|e| format!("zvec-grep config.json is invalid: {e}"))?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Value::Null,
            Err(e) => return Err(format!("zvec-grep config.json is invalid: {e}")),
        };
        validate_version(&raw)?;
        Ok(Self::normalize(&raw))
    }
    pub fn normalize(raw: &Value) -> Self {
        let mut c = Self::default();
        if !trim(string(&raw["embedding"])).is_empty() {
            c.embedding = trim(string(&raw["embedding"])).into();
        }
        let device = js_string(&raw["device"]);
        if ["auto", "cpu", "metal", "vulkan", "cuda"].contains(&device.as_str()) {
            c.device = device;
        }
        let mode = js_string(&raw["mode"]);
        if ["in-process", "external-daemon"].contains(&mode.as_str()) {
            c.mode = mode;
        }
        c.max_results = clamp(raw.get("maxResults"), 1, 20, c.max_results);
        c.timeout_ms = clamp(raw.get("searchTimeoutMs"), 5_000, 115_000, c.timeout_ms);
        if raw["ignoredGlobs"].is_array() {
            for s in strings(&raw["ignoredGlobs"]) {
                if !c.ignored_globs.contains(&s) {
                    c.ignored_globs.push(s);
                }
                if c.ignored_globs.len() == 50 {
                    break;
                }
            }
        }
        c.notify = raw["notifyOnIndexComplete"] == true;
        c
    }
    pub fn local(&self) -> bool {
        self.embedding.starts_with("local/")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn settings_keep_clamps_policy_and_remote_device_rule() {
        let config = Config::normalize(
            &json!({"version":1,"embedding":" qwen/text-embedding-v4 ","device":"cpu","mode":"external-daemon","maxResults":100,"ignoredGlobs":["  **/dist/**  ","**/dist/**",null,""],"searchTimeoutMs":1,"notifyOnIndexComplete":true}),
        );
        assert_eq!(config.embedding, "qwen/text-embedding-v4");
        assert!(!config.local());
        assert_eq!(config.max_results, 20);
        assert_eq!(config.timeout_ms, 5000);
        assert_eq!(config.ignored_globs, ["**/dist/**"]);
        assert!(config.notify);
    }
    #[test]
    fn numeric_coercion_matches_ecmascript_number() {
        for (input, expected) in [
            (json!(""), 0.0),
            (json!(" \t\u{feff}\u{2003}"), 0.0),
            (json!("0x10"), 16.0),
            (json!("0B11"), 3.0),
            (json!("0o17"), 15.0),
            (json!(" +2.9e1 "), 29.0),
            (json!([]), 0.0),
            (json!([null]), 0.0),
            (json!([""]), 0.0),
            (json!([["17"]]), 17.0),
        ] {
            assert_eq!(number(&input), Some(expected), "{input}");
        }
        for input in [
            json!([true]),
            json!([1, 2]),
            json!({}),
            json!("\u{0085}"),
            json!("1\u{0085}"),
            json!("-0x2"),
            json!("0b102"),
            json!("0x"),
            json!("1_0"),
            json!("inf"),
            json!("1e+"),
        ] {
            assert_eq!(number(&input), None, "{input}");
        }
        assert_eq!(clamp(Some(&json!("Infinity")), 1, 20, 5), 5);
        assert_eq!(clamp(None, 1, 20, 5), 5);
        assert_eq!(clamp(Some(&Value::Null), 1, 20, 5), 1);
    }
    #[test]
    fn settings_preserve_string_coercion_and_ecmascript_trim() {
        let c = Config::normalize(
            &json!({"embedding":"\u{feff}local/custom\u{3000}","device":[["cpu"]],"mode":["external-daemon"],"maxResults":"","searchTimeoutMs":["0x1388"],"ignoredGlobs":["\u{feff}**/dist/**\u{2003}","**/dist/**","\u{0085}"]}),
        );
        assert_eq!(c.embedding, "local/custom");
        assert_eq!(c.device, "cpu");
        assert_eq!(c.mode, "external-daemon");
        assert_eq!(c.max_results, 1);
        assert_eq!(c.timeout_ms, 5000);
        assert_eq!(c.ignored_globs, ["**/dist/**", "\u{0085}"]);
        let c = Config::normalize(
            &json!({"maxResults":["0x10"],"searchTimeoutMs":"\u{feff} 9e4 \u{202f}"}),
        );
        assert_eq!(c.max_results, 16);
        assert_eq!(c.timeout_ms, 90_000);
    }
    #[test]
    fn state_version_requires_a_supported_integer_header() {
        for raw in [
            "{}",
            "{\"version\":null}",
            "{\"version\":0}",
            "{\"version\":1}",
        ] {
            assert!(
                validate_version(&serde_json::from_str::<Value>(raw).unwrap()).is_ok(),
                "{raw}"
            );
        }
        for version in [
            json!(1.0),
            json!(1.5),
            json!("1"),
            json!(true),
            json!([]),
            json!({}),
            json!(-1),
            json!(u64::MAX),
        ] {
            assert!(
                validate_version(&json!({"version":version}))
                    .unwrap_err()
                    .contains("invalid")
            );
        }
        assert!(
            validate_version(&serde_json::from_str::<Value>("{\"version\":1e0}").unwrap()).is_err()
        );
        assert!(
            validate_version(&json!({"version":2}))
                .unwrap_err()
                .contains("newer state version")
        );
    }
}
