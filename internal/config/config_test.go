package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func TestLoadMergesGlobalProjectAndEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("OPENAI_API_KEY", "from-env")
	t.Setenv("ANTHROPIC_API_KEY", "")

	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte(`
[defaults]
provider = "anthropic"
model = "claude-sonnet-4-5"

[providers.anthropic]
api_key = "global-ant"
base_url = "https://api.anthropic.com"

[providers.openai]
api_key = "global-oai"

[compaction]
reserve_tokens = 1000
max_context_tokens = 25000
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".ki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".ki", "ki.toml"), []byte(`
[defaults]
model = "claude-opus-4"

[providers.anthropic]
api_key = "project-ant"
api = "anthropic"

[server]
addr = "127.0.0.1:19999"

[log]
level = "debug"
max_size_mb = 2
max_backups = 4
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Provider != "anthropic" {
		t.Fatalf("provider: %q", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "claude-opus-4" {
		t.Fatalf("project should override model, got %q", cfg.Defaults.Model)
	}
	if cfg.Providers["anthropic"].APIKey != "project-ant" {
		t.Fatalf("project key: %q", cfg.Providers["anthropic"].APIKey)
	}
	if cfg.Providers["openai"].APIKey != "from-env" {
		t.Fatalf("env should override openai key, got %q", cfg.Providers["openai"].APIKey)
	}
	if cfg.Server.Addr != "127.0.0.1:19999" {
		t.Fatalf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Log.Level != "debug" || cfg.Log.MaxSizeMB != 2 || cfg.Log.MaxBackups != 4 {
		t.Fatalf("log: %+v", cfg.Log)
	}
	if cfg.Compaction.ReserveTokens != 1000 {
		t.Fatalf("reserve: %d", cfg.Compaction.ReserveTokens)
	}
	if cfg.Compaction.MaxContextTokens != 25000 {
		t.Fatalf("max_context_tokens: %d", cfg.Compaction.MaxContextTokens)
	}
	if cfg.Providers["anthropic"].API != "anthropic" {
		t.Fatalf("api override: %q", cfg.Providers["deepseek"].API)
	}
	if cfg.Providers["anthropic"].BaseURL != "https://api.anthropic.com" {
		t.Fatalf("project merge should preserve global base url: %q", cfg.Providers["anthropic"].BaseURL)
	}
	if cfg.Sessions.Root != filepath.Join(home, "sessions") {
		t.Fatalf("sessions root: %q", cfg.Sessions.Root)
	}
}

func TestLoadRejectsInvalidAndUnknownTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	path := filepath.Join(home, "ki.toml")

	if err := os.WriteFile(path, []byte("[server\naddr = \"127.0.0.1:1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("invalid TOML should return an error")
	}

	if err := os.WriteFile(path, []byte("[server]\nunknown = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("unknown configuration key should return an error")
	}
}

func TestLoadWithViperFlagOverridesEnvAndTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("KI_SERVER_ADDR", "127.0.0.1:20001")
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte("[server]\naddr = \"127.0.0.1:20002\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("addr", "", "listen address")
	if err := flags.Set("addr", "127.0.0.1:20003"); err != nil {
		t.Fatal(err)
	}
	settings := viper.New()
	if err := settings.BindPFlag("server.addr", flags.Lookup("addr")); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithViper(t.TempDir(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:20003" {
		t.Fatalf("flag should override env and TOML, got %q", cfg.Server.Addr)
	}
}

func TestLoadMissingFilesUsesBuiltin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 16384 {
		t.Fatalf("builtin compaction: %+v", cfg.Compaction)
	}
	if cfg.Server.Addr != "127.0.0.1:19800" {
		t.Fatalf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Log.Level != "info" || cfg.Log.MaxSizeMB != 10 || cfg.Log.MaxBackups != 3 {
		t.Fatalf("builtin log: %+v", cfg.Log)
	}
}
