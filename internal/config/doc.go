// Package config loads and merges Ki configuration through Viper.
//
// Order (low to high): compiled defaults, ~/.ki/ki.toml (KI_HOME overrides
// the home), <cwd>/.ki/ki.toml, then KI_SERVER_ADDR.
// Session config.json is owned by package session, not this package.
// CLI --model does not write toml.
//
// Provider/model settings and credentials are owned by package provider in
// models.json and credentials.json, not TOML.
package config
