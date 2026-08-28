import { createHash } from "node:crypto";
import { compactText, tokenSimilarity, tokens, canonicalUrl, hostOf } from "./normalize.js";

function hash(value) {
  return createHash("sha256").update(value).digest("hex");
}

export function classifySource(url) {
  const host = hostOf(url);
  if (!host) return { kind: "unknown", quality: 0.2, reason: "invalid-url" };
  if (/^(github\.com|gitlab\.com|bitbucket\.org)$/.test(host) || host.endsWith(".gov") || host.endsWith(".edu")) return { kind: "primary", quality: 0.95, reason: "primary-host" };
  if (/wikipedia\.org$/.test(host) || /docs?\./.test(host)) return { kind: "reference", quality: 0.8, reason: "reference-host" };
  if (/reddit\.com$|quora\.com$|medium\.com$/.test(host)) return { kind: "community", quality: 0.55, reason: "community-host" };
  return { kind: "secondary", quality: 0.68, reason: "general-web" };
}

function sentences(content) {
  return compactText(content, 50_000).split(/(?<=[.!?。！？])\s+/u).map((text) => text.trim()).filter((text) => text.length >= 20);
}

export function buildEvidence(result) {
  const sources = (result.results || []).map((item, index) => {
    const url = canonicalUrl(item.url) || item.url;
    const passages = [];
    const raw = item.content || item.snippet || "";
    for (const sentence of sentences(raw).slice(0, 12)) {
      const offset = raw.indexOf(sentence);
      passages.push({ text: sentence, start: Math.max(0, offset), end: Math.max(0, offset) + sentence.length, hash: hash(sentence) });
    }
    if (!passages.length && item.snippet) passages.push({ text: compactText(item.snippet, 500), start: 0, end: compactText(item.snippet, 500).length, hash: hash(item.snippet) });
    return {
      sourceId: `source-${index + 1}`,
      url,
      title: item.title,
      provider: item.providers || [item.provider],
      classification: classifySource(url),
      passages,
    };
  });
  return { query: result.query, responseId: result.responseId, sources };
}

function overlap(claim, passage) {
  const left = tokens(claim);
  const right = tokens(passage);
  if (!left.size || !right.size) return 0;
  let shared = 0;
  for (const item of left) if (right.has(item)) shared++;
  return shared / left.size;
}

const CONTRADICTION_WORDS = ["not", "never", "false", "incorrect", "无", "不是", "错误", "否认", "并非"];

export function assessClaim(claim, evidence) {
  const candidates = [];
  for (const source of evidence.sources || []) {
    for (const passage of source.passages || []) {
      const score = overlap(claim, passage.text);
      if (score >= 0.25) candidates.push({ source, passage, score });
    }
  }
  candidates.sort((a, b) => b.score - a.score);
  const best = candidates[0];
  const contradiction = candidates.some((item) => {
    const lower = item.passage.text.toLowerCase();
    return CONTRADICTION_WORDS.some((word) => lower.includes(word)) && item.score >= 0.45;
  });
  let status = "missing-evidence";
  if (best && contradiction) status = "contradicted";
  else if (best && best.score >= 0.62) status = "supported";
  else if (best) status = "unclear";
  return {
    claim,
    status,
    score: best ? Number(best.score.toFixed(3)) : 0,
    sources: candidates.slice(0, 5).map((item) => ({ sourceId: item.source.sourceId, url: item.source.url, passageHash: item.passage.hash, score: Number(item.score.toFixed(3)) })),
    method: "deterministic-token-overlap",
  };
}

export function sourceCheckClaims(claims, evidence) {
  return claims.map((claim) => assessClaim(claim, evidence));
}
