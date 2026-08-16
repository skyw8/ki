// Package loop is the agent main loop: emit events, await hooks.
//
// It does not write disk, speak HTTP, or assemble prompt files. Subscribers
// (persist, SSE) are attached by the server via emit.
//
// Events follow pi names: agent_*, turn_*, message_*, tool_execution_*,
// plus request_header (system + tools snapshot after turn_start, before stream).
// BeforeTool errors fail closed (block the tool). Tools default to parallel.
// Provider errors retry up to 5 times with exponential backoff from 2s.
//
// Event order: docs/architecture.md.
package loop
