// Package telemetry writes session-local harness diagnostics as OTLP/JSON.
//
// Each line in telemetry.jsonl is an ExportLogsServiceRequest. Records contain
// hashes, counters, classifications, and correlation IDs only: prompt bodies,
// tool arguments/results, credentials, and provider-owned opaque data are never
// recorded. Telemetry is best-effort and must not affect a session run.
package telemetry
