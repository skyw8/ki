// Package e2e exercises the CLI/server runtime with isolated session homes.
// Default tests use the scripted provider; live-tagged tests use configured
// DeepSeek protocols and the Chromium WebUI. Image/PDF checks pair persisted
// read calls with successful tool results for every fixture path, rather than
// treating model-request schemas as evidence of tool execution.
// Code Mode process checks reuse the real CLI binary, so worker entrypoint
// dispatch and stdout IPC framing cannot be hidden by test-only worker hooks.
package e2e
