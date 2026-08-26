# Codex OAuth provider extension

This directory is a Ki provider extension source package. It is already under
the project extension directory. The sidecar is started directly by `uv`:

```bash
cd .ki/extensions/codex-oauth
uv run --project . main.py
```

Restart or reload Ki after changing the source. The provider appears as `openai-codex` in
the provider settings. Use Browser login locally, or Device code login when
the WebUI is reached through a port forward. `KI_CODEX_AUTH_BASE_URL` and
`KI_CODEX_CALLBACK_PORT` are test-only endpoint overrides; normal use talks to
the OpenAI Codex service endpoints defined in `extension.json`.
