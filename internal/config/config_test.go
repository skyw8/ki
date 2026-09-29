package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func TestLoadMergesGlobalProjectAndEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("KI_SERVER_ADDR", "127.0.0.1:18888")

	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte(`
[compaction]
reserve_tokens = 1000
max_context_tokens = 25000
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".ki"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".ki", "ki.toml"), []byte(`
[server]
addr = "127.0.0.1:19999"

[log]
level = "debug"
max_size_mb = 2
max_backups = 4
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:18888" {
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
	if cfg.Sessions.Root != filepath.Join(home, "sessions") {
		t.Fatalf("sessions root: %q", cfg.Sessions.Root)
	}
}

func TestLoadRejectsInvalidAndUnknownTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	path := filepath.Join(home, "ki.toml")

	if err := os.WriteFile(path, []byte("[server\naddr = \"127.0.0.1:1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("invalid TOML should return an error")
	}

	if err := os.WriteFile(path, []byte("[server]\nunknown = true\n"), 0o600); err != nil {
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
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte("[server]\naddr = \"127.0.0.1:20002\"\n"), 0o600); err != nil {
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
	if !cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 16384 ||
		cfg.Compaction.Mode != "auto" || !cfg.Compaction.ServerSide {
		t.Fatalf("builtin compaction: %+v", cfg.Compaction)
	}
	if cfg.Server.Addr != "127.0.0.1:19800" {
		t.Fatalf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Streaming.IdleTimeoutSeconds != 300 {
		t.Fatalf("streaming: %+v", cfg.Streaming)
	}
	if cfg.Log.Level != "info" || cfg.Log.MaxSizeMB != 10 || cfg.Log.MaxBackups != 3 {
		t.Fatalf("builtin log: %+v", cfg.Log)
	}
}

func TestLoadRejectsInvalidCompactionMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte("[compaction]\nmode = \"magic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "compaction.mode") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestStreamingIdleTimeout(t *testing.T) {
	for _, seconds := range []int{-1, 0, 42} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KI_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "ki.toml"), fmt.Appendf(nil, "[streaming]\nidle_timeout_seconds = %d\n", seconds), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(t.TempDir())
			if seconds < 0 {
				if err == nil {
					t.Fatal("negative timeout accepted")
				}
			} else if err != nil || cfg.Streaming.IdleTimeoutSeconds != seconds {
				t.Fatalf("timeout: %+v, error: %v", cfg.Streaming, err)
			}
		})
	}
}
