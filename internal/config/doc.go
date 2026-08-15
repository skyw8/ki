// Package config loads and merges Ki configuration.
//
// Order (low to high): compiled defaults, ~/.ki/ki.toml (KI_HOME overrides
// the home), <cwd>/.ki/ki.toml, then env for api_key and KI_SERVER_ADDR only.
// Session config.json is owned by package session, not this package.
// CLI --model does not write toml.
//
// The parser is a small TOML subset (sections and key = value).
package config
