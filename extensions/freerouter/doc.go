// Command freerouter races OpenRouter's free assistant models and exposes an
// OpenAI-compatible HTTP proxy, optionally alongside Ki's NDJSON provider RPC.
// A compiled executable contains the complete runtime; external interpreters
// and source files are never used. Config updates reset cooldowns but require a
// sidecar restart to change its listening address. Losing streams are canceled,
// and empty or reasoning-only output cannot win a race. Cancellation takes
// precedence over simultaneously queued stream completions and timeouts, so an
// aborted winner is never reported as stalled or put on cooldown. See README.md.
package main
