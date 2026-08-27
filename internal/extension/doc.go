// Package extension discovers global extension packages and runs their
// language-agnostic JSON-RPC sidecars.
//
// A package is a directory with extension.json under {KI_HOME}/extensions.
// Declarative contributions (prompt, skills, and slash templates) are global.
// Code capabilities (tools, lifecycle subscriptions, executable slash) run in
// one NDJSON sidecar per enabled package, owned by the server process. Provider
// capabilities use the same process-level lifetime and are shared by all
// sessions.
// Channel sidecars can call session.appendMessage to persist a normal user
// message without starting a run; session.appendEntry remains custom and is
// not model-facing.
// Provider OAuth progress also uses that process-level sidecar: URL/device-code
// events are UI-neutral and completed opaque credentials return only through
// the server auth broker.
// Enablement is toggles.json extensions.disabled
// (missing = all on). Host interceptors are test doubles only; production
// never compiles user extensions into the ki binary.
// Runtime failures are reported through the server's extension_error event;
// the server disables the failing package in toggles.json.
// Cross-package contract: docs/extension.md.
package extension
