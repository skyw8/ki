# Codex OAuth provider extension

This directory is a Ki provider extension source package. Install or copy it
under `{KI_HOME}/extensions/codex-oauth` for Ki to discover it globally. The
sidecar is started directly by `uv`:

```bash
cd extensions/codex-oauth
uv run --project . main.py
```

Restart or reload Ki after changing the source. The provider appears as `openai-codex` in
the provider settings. Use Browser login locally, or Device code login when
the WebUI is reached through a port forward. `KI_CODEX_AUTH_BASE_URL` and
`KI_CODEX_CALLBACK_PORT` are test-only endpoint overrides; normal use talks to
the OpenAI Codex service endpoints defined in `extension.json`.

## Context windows

`contextWindow` follows the OpenAI Codex bundled catalog
(`codex-rs/models-manager/models.json`), which caps `gpt-6-astra` and the
GPT-5.6 Sol/Terra/Luna variants at 272,000 tokens for coding clients
(`max_context_window` 872,000 is a configuration override, not the default).
The model API spec pages advertise 1,050,000, but the ChatGPT-backed Codex
service this extension talks to enforces the smaller window. The built-in
`openai` catalog entries use the same value so a Codex-issued credential and a
direct API key see one consistent context window.

## Thinking levels

`thinkingLevelMap` mirrors the built-in catalog entries for the same models.
No GPT-6/5.6 variant accepts `minimal`, so every map hides it. The GPT-5.6
models accept `none` (the API lists `none, low, medium, high, xhigh, max`), so
`off` stays visible and maps to the zero-reasoning floor; the sidecar skips the
whole `reasoning` block for `off` anyway. GPT-6 Astra rejects `none` (its
`reasoning.effort` supports only `low, medium, high, xhigh, max`), so its map
hides `off` as well.
