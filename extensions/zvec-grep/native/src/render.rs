use crate::config::string;
use serde_json::Value;

fn truncate(text: &str, max: usize) -> String {
    let text = text.encode_utf16().collect::<Vec<_>>();
    if text.len() <= max {
        String::from_utf16_lossy(&text)
    } else {
        format!("{}…", String::from_utf16_lossy(&text[..max]))
    }
}
pub fn clamp(text: &str, max: usize) -> String {
    let units = text.encode_utf16().collect::<Vec<_>>();
    if units.len() <= max {
        text.into()
    } else {
        format!(
            "{}\n… (output truncated at {max} characters)",
            String::from_utf16_lossy(&units[..max])
        )
    }
}
fn truncate_lines(text: &str, max: usize) -> String {
    let lines = text.split('\n').collect::<Vec<_>>();
    if lines.len() <= max {
        text.into()
    } else {
        format!(
            "{}\n… ({} more lines)",
            lines[..max].join("\n"),
            lines.len() - max
        )
    }
}
pub fn number_lines(text: &str, start: u64) -> Vec<String> {
    text.strip_suffix('\n')
        .unwrap_or(text)
        .split('\n')
        .enumerate()
        .map(|(i, line)| format!("{}\t{line}", start + i as u64))
        .collect()
}
pub fn render(result: &Value, full: bool) -> String {
    let items = result["items"].as_array().map(Vec::as_slice).unwrap_or(&[]);
    let coverage = string(&result["coverage"]);
    let freshness = if coverage == "ranked_sample" {
        if items.iter().any(|v| v["status"] == "possibly_stale") {
            "possibly_stale"
        } else {
            "fresh"
        }
    } else {
        coverage
    };
    let mut out = vec![
        format!("freshness: {freshness}"),
        format!("root: {}", string(&result["root"])),
        format!("source: {}", string(&result["source"])),
        format!("coverage: {coverage}"),
        format!("items: {}", items.len()),
    ];
    if let Some(routes) = result["diagnostics"]["index"]["routes"]
        .as_array()
        .filter(|v| !v.is_empty())
    {
        out.push(format!(
            "routes: {}",
            routes
                .iter()
                .map(|r| format!(
                    "{}:{}",
                    string(&r["mode"]),
                    truncate(string(&r["query"]), 80)
                ))
                .collect::<Vec<_>>()
                .join(" | ")
        ));
    }
    if result["diagnostics"]["rg"]["truncated"] == true {
        out.push("note: ripgrep output was truncated by the result limit".into());
    }
    if result["diagnostics"]["structure"]["truncated"] == true {
        out.push(format!(
            "note: structural enrichment stopped early ({} enriched items)",
            result["diagnostics"]["structure"]["enrichedItems"]
        ));
    }
    if items.is_empty() {
        out.push("".into());
        out.push(
            if result["diagnostics"]["emptyReason"] == "no_searchable_files" {
                "No searchable files matched the scope filters for this workspace."
            } else {
                "No matches. Try different wording, or use Grep for an exact anchor."
            }
            .into(),
        );
    }
    for (index, item) in items.iter().enumerate() {
        out.push("".into());
        let start = item["range"]["startLine"].as_u64().unwrap_or(1);
        let end = item["range"]["endLine"].as_u64().unwrap_or(start);
        let mut flags = vec![
            format!("#{}", index + 1),
            format!("rank={}", item["rank"]),
            format!("matchedBy={}", string(&item["matchedBy"])),
        ];
        if let Some(score) = item["score"].as_f64() {
            flags.push(format!("score={score:.4}"));
        }
        if item["status"] == "possibly_stale" {
            flags.push("possibly_stale".into());
        }
        out.push(format!(
            "{} {}:{start}-{end}",
            flags.join(" "),
            string(&item["file"]["relativePath"])
        ));
        let m = &item["metadata"];
        if let Some(heading) = m["heading"].as_str() {
            out.push(format!(
                "heading: {heading}{}",
                m["level"]
                    .as_u64()
                    .map(|n| format!(" (level {n})"))
                    .unwrap_or_default()
            ));
        }
        if let Some(kind) = m["symbolType"].as_str() {
            out.push(format!(
                "symbol: {kind}{}",
                m["name"]
                    .as_str()
                    .map(|s| format!(" {s}"))
                    .unwrap_or_default()
            ));
        }
        if let Some(container) = item.get("container").filter(|v| !v.is_null()) {
            if container["range"]["startLine"] != start {
                out.push(format!(
                    "container: {}-{}",
                    container["range"]["startLine"], container["range"]["endLine"]
                ));
            }
        }
        if let Some(groups) = item["queryGroups"].as_array().filter(|v| !v.is_empty()) {
            out.push(format!(
                "groups: {}",
                groups
                    .iter()
                    .map(|g| format!("{}({})", string(&g["id"]), g["rank"]))
                    .collect::<Vec<_>>()
                    .join(", ")
            ));
        }
        let content = truncate_lines(
            &clamp(string(&item["content"]), if full { 8000 } else { 1000 }),
            if full { 80 } else { 12 },
        );
        if !content.is_empty() {
            out.push("source:".into());
            out.extend(number_lines(
                &content,
                item["contentStartLine"].as_u64().unwrap_or(start),
            ));
        }
        if full && item["contentRole"] != "outline" && !string(&item["outline"]).is_empty() {
            out.push("outline:".into());
            out.extend(number_lines(
                &truncate_lines(string(&item["outline"]), 40),
                start,
            ));
        }
    }
    clamp(&out.join("\n"), 24000)
}
