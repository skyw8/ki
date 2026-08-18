// Package config loads and merges Ki configuration through Viper.
//
// Order (low to high): compiled defaults, ~/.ki/ki.toml (KI_HOME overrides
// the home), <cwd>/.ki/ki.toml, then env for api_key and KI_SERVER_ADDR only.
// Session config.json is owned by package session, not this package.
// CLI --model does not write toml.
//
// TOML syntax and types are parsed by Viper; this package owns the layered
// merge and the provider-specific environment variable aliases.
package config
