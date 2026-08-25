// Package extension discovers local extension packages and runs their
// language-agnostic JSON-RPC sidecars.
//
// A package is a directory with extension.json under {KI_HOME}/extensions or
// <cwd>/.ki/extensions. Declarative contributions (prompt, skills, slash
// templates, MCP specs) merge into the session snapshot. Code capabilities
// (tools, lifecycle subscriptions, executable slash) run in one NDJSON
// sidecar per enabled package, started when that session is opened (not on
// List or serve boot). Enablement is toggles.json extensions.disabled
// (missing = all on). Host interceptors are test doubles only; production
// never compiles user extensions into the ki binary.
// Cross-package contract: docs/extension.md.
package extension
