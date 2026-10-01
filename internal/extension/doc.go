// Package extension discovers global extension packages and runs their
// language-agnostic JSON-RPC sidecars.
//
// A package is a directory with extension.json under {KI_HOME}/extensions.
// Declarative contributions (prompt, skills, slash templates, and runtime.path
// shell PATH directories) are global.
// Optional extension-owned i18n resources are loaded into the read-only catalog
// and remain opaque to the host; the WebUI resolves their UIText values.
// The catalog also lists loaded skills, sidecar tools/commands, prompt-append
// files, and declared providers so session Info can show what a package contributed.
// Code capabilities (tools, lifecycle subscriptions, executable slash) run in
// one NDJSON sidecar per enabled package, owned by the server process. Provider
// capabilities use the same process-level lifetime and are shared by all
// sessions.
// Provider sidecars that advertise compaction.standalone implement
// provider.compact. Inline Responses items require a separate inline capability
// and remain outside ordinary message/lifecycle JSON.
// session_before_compact receives a portable host preparation after planning;
// it may cancel or replace local summary generation, but cannot alter the cut.
// A synchronous hook that can change provider-visible messages or routing
// forces portable history replay because it cannot inspect an encrypted prefix.
// Channel sidecars can call session.appendMessage to persist a normal user
// message without starting a run; session.appendEntry remains custom and is
// not model-facing.
// Provider OAuth progress also uses that process-level sidecar: URL/device-code
// events are UI-neutral and completed opaque credentials return only through
// the server auth broker.
// Enablement is toggles.json extensions.disabled
// (missing = all on). Host interceptors are test doubles only; production
// never compiles user extensions into the ki binary.
// Manifest errors make a package unavailable in the catalog and exclude it
// from runtime configuration. Sidecar start failures and undeclared
// capabilities are reported and skipped for the affected runtime or
// registration, while the package's manual toggles remain separate. Occupy
// RPC timeouts stay fail-open: the package is skipped for that occupy and is
// not toggled off.
// Install commands, sidecars, and their descendants inherit Ki's proxy
// environment; runtime.env can explicitly override those variables.
// Install hooks inherit the full parent build environment, then apply scoped
// KI_* values and runtime.env overrides so arbitrary language toolchains keep
// their configured caches and native compiler paths. Sidecars retain the
// platform/profile/temp/proxy environment allowlist and explicit runtime.env.
// Environment overrides preserve case-sensitive keys on Unix and match keys
// case-insensitively on Windows.
// Install hooks run before every sidecar start by default or with
// runtime.installWhen=always. With installWhen=missing, a package-relative RPC
// executable (including its Windows .exe suffix) skips installation when
// already present, allowing source-free packages to launch without a toolchain.
// Bundled Go executables (and the Rust search executable) launch directly from
// source-free distribution packages; no compiler or interpreter is required.
// This does not restrict third-party extension implementation languages.
// Private config uses atomic versioned state; the storage header is excluded
// from settings schema validation and public/redacted configuration values.
// Tool contracts come from internal/tool; reserved builtin identifiers come
// from its lightweight builtin/catalog. Sidecar process-tree control comes from
// internal/process, without depending on builtin tool implementations.
// Tool registration is atomic against canonical snake_case/PascalCase/native
// aliases and built-in reserved names. Static names are global; dynamic names
// collide only within one session's combined registry. Model schema/catalog and
// lifecycle hooks use canonical names while tool.execute RPC retains native names.
// ContextOnly is host metadata and is removed from sidecar message projections.
// Cross-package contract: docs/extension.md.
package extension
