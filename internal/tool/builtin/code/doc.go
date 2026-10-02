// Package codetools adapts Code Mode sessions to the ordinary tool contract.
//
// Exec accepts raw JavaScript for Responses custom tools and a code string for
// JSON function tools; both share the first-line Codex pragma. Wait observes
// incremental cell output. Tool definitions are built from the immutable,
// filtered nested tool set, never from an unrestricted registry. Token budgets
// use Ki's four-UTF-8-bytes-per-token approximation with runtime byte caps.
// Deferred declarations may be omitted from exec prompts, but the same full
// allowed definitions still populate runtime tools and ALL_TOOLS. search_tool
// is a model-facing discovery entrypoint and is never a nested JS capability.
package codetools
