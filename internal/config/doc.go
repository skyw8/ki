// Package config loads and merges Ki configuration through Viper.
//
// Order (low to high): compiled defaults, ~/.ki/ki.toml (KI_HOME overrides
// the home), <cwd>/.ki/ki.toml, then KI_SERVER_ADDR, KI_EXTENSIONS_SOURCE and
// KI_EXTENSIONS_RELEASE.
// Session config.json is owned by package session, not this package.
// CLI --model does not write toml.
// extensions.release selects the release path whose assets are downloaded by the
// WebUI installer: a release build pins download/<its tag>, a local build follows
// latest/download. extensions.source is the release base URL, defaulting to the
// project's GitHub releases. A file:// source serves a local mirror and works
// offline.
// streaming.idle_timeout_seconds bounds native provider response-body reads
// (default 300, zero disables); it is not a whole-request timeout.
// compaction.mode selects auto, local, or remote checkpoints;
// compaction.server_side enables provider-managed Responses compaction for
// catalog models that explicitly advertise it.
// Code Mode always exposes direct tools plus exec/wait (mixed); it has no
// TOML setting.
// MCP configuration lives separately in version-1 mcp.json documents under
// KI_HOME and <cwd>/.ki. Project entries replace whole global entries with the
// same case-sensitive name. Camel-case mcpServers fields select official-SDK
// transports and raw-tool policies; environment keys retain their exact case.
// Missing documents are optional, malformed/newer documents fail loading, and
// legacy mcp_servers TOML tables are rejected rather than silently ignored.
//
// agents.max_concurrent defaults to four active child turns per root; the root
// itself is excluded and waiting child turns still count. There is no depth cap.
// Provider/model settings and credentials are owned by package provider in
// models.json and credentials.json, not TOML.
package config
