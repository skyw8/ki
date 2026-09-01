// Package extension discovers global extension packages and runs their
// language-agnostic JSON-RPC sidecars.
//
// A package is a directory with extension.json under {KI_HOME}/extensions.
// Declarative contributions (prompt, skills, and slash templates) are global.
// Optional extension-owned i18n resources are loaded into the read-only catalog
// and remain opaque to the host; the WebUI resolves their UIText values.
// The catalog also lists loaded skills, sidecar tools/commands, prompt-append
// files, and declared providers so session Info can show what a package contributed.
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
// Manifest errors, sidecar start failures, and undeclared capabilities disable
// the package in toggles.json. Occupy RPC timeouts stay fail-open: the package
// is skipped for that occupy and is not toggled off.
// Install commands, sidecars, and their descendants inherit Ki's proxy
// environment; runtime.env can explicitly override those variables.
// Cross-package contract: docs/extension.md.
package extension
