package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"

	"ki/internal/buildinfo"
)

type Config struct {
	Agents       Agents               `mapstructure:"agents"`
	Home         string               `mapstructure:"-"`
	Sessions     Sessions             `mapstructure:"sessions"`
	Compaction   Compaction           `mapstructure:"compaction"`
	Server       Server               `mapstructure:"server"`
	Log          Log                  `mapstructure:"log"`
	Push         Push                 `mapstructure:"push"`
	Streaming    Streaming            `mapstructure:"streaming"`
	Extensions   Extensions           `mapstructure:"extensions"`
	MCPServers   map[string]MCPServer `mapstructure:"-"`
	MCPFromFiles bool                 `mapstructure:"-"`
}

type Agents struct {
	MaxConcurrent int `mapstructure:"max_concurrent"`
}

type Streaming struct {
	IdleTimeoutSeconds int `mapstructure:"idle_timeout_seconds"`
}

type Extensions struct {
	Source  string `mapstructure:"source"`
	Release string `mapstructure:"release"`
}

const DefaultExtensionSource = "https://github.com/skyw8/ki/releases"

func extensionReleasePath() string {
	if tag := strings.TrimSpace(buildinfo.ReleaseTag); tag != "" {
		return "download/" + tag
	}
	return "latest/download"
}

type Sessions struct {
	Root string `mapstructure:"root"`
}

type Compaction struct {
	Enabled          bool   `mapstructure:"enabled"`
	ReserveTokens    int    `mapstructure:"reserve_tokens"`
	KeepRecentTokens int    `mapstructure:"keep_recent_tokens"`
	Mode             string `mapstructure:"mode"`
	ServerSide       bool   `mapstructure:"server_side"`
	MaxContextTokens int    `mapstructure:"max_context_tokens"`
}

type Server struct {
	Addr string `mapstructure:"addr"`
}

type Log struct {
	Level      string `mapstructure:"level"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
}

type Push struct {
	Enabled bool   `mapstructure:"enabled"`
	Subject string `mapstructure:"subject"`
}

func Builtin(home string) Config {
	if home == "" {
		home = HomeDir()
	}
	return Config{
		Agents: Agents{MaxConcurrent: 4},
		Home:   home,
		Sessions: Sessions{
			Root: filepath.Join(home, "sessions"),
		},
		Compaction: Compaction{
			Enabled:          true,
			ReserveTokens:    16384,
			KeepRecentTokens: 20000,
			Mode:             "auto",
			ServerSide:       true,
		},
		Server:    Server{Addr: "127.0.0.1:19800"},
		Log:       Log{Level: "info", MaxSizeMB: 10, MaxBackups: 3},
		Push:      Push{Enabled: true, Subject: "mailto:ki@localhost"},
		Streaming: Streaming{IdleTimeoutSeconds: 300},
		Extensions: Extensions{
			Source:  DefaultExtensionSource,
			Release: extensionReleasePath(),
		},
	}
}

func HomeDir() string {
	if h := strings.TrimSpace(os.Getenv("KI_HOME")); h != "" {
		return h
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return ".ki"
	}
	return filepath.Join(user, ".ki")
}

func Load(cwd string) (Config, error) {
	return LoadWithViper(cwd, nil)
}

func LoadWithViper(cwd string, settings *viper.Viper) (Config, error) {
	home := HomeDir()
	builtin := Builtin(home)
	if settings == nil {
		settings = viper.New()
	}
	setDefaults(settings, builtin)
	global := filepath.Join(home, "ki.toml")
	if err := mergeFile(settings, global); err != nil {
		return Config{}, err
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd != "" {
		if err := mergeFile(settings, filepath.Join(cwd, ".ki", "ki.toml")); err != nil {
			return Config{}, err
		}
	}
	if err := mergeEnv(settings); err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := settings.UnmarshalExact(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.Home = home
	mcpSnapshot, err := LoadMCPSnapshot(home, cwd)
	if err != nil {
		return Config{}, err
	}
	cfg.MCPServers = mcpSnapshot.Servers
	cfg.MCPFromFiles = mcpSnapshot.FilesPresent
	if cfg.Agents.MaxConcurrent < 1 {
		return Config{}, fmt.Errorf("agents.max_concurrent must be positive")
	}
	if cfg.Streaming.IdleTimeoutSeconds < 0 {
		return Config{}, fmt.Errorf("streaming.idle_timeout_seconds must not be negative")
	}
	switch cfg.Compaction.Mode {
	case "auto", "local", "remote":
	default:
		return Config{}, fmt.Errorf("compaction.mode must be auto, local, or remote")
	}
	if cfg.Sessions.Root == "" {
		cfg.Sessions.Root = filepath.Join(cfg.Home, "sessions")
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:19800"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = builtin.Log.Level
	}
	return cfg, nil
}

func setDefaults(settings *viper.Viper, cfg Config) {
	settings.SetDefault("agents.max_concurrent", cfg.Agents.MaxConcurrent)
	settings.SetDefault("sessions.root", cfg.Sessions.Root)
	settings.SetDefault("compaction.enabled", cfg.Compaction.Enabled)
	settings.SetDefault("compaction.reserve_tokens", cfg.Compaction.ReserveTokens)
	settings.SetDefault("compaction.keep_recent_tokens", cfg.Compaction.KeepRecentTokens)
	settings.SetDefault("compaction.max_context_tokens", cfg.Compaction.MaxContextTokens)
	settings.SetDefault("compaction.mode", cfg.Compaction.Mode)
	settings.SetDefault("compaction.server_side", cfg.Compaction.ServerSide)
	settings.SetDefault("server.addr", cfg.Server.Addr)
	settings.SetDefault("log.level", cfg.Log.Level)
	settings.SetDefault("log.max_size_mb", cfg.Log.MaxSizeMB)
	settings.SetDefault("log.max_backups", cfg.Log.MaxBackups)
	settings.SetDefault("push.enabled", cfg.Push.Enabled)
	settings.SetDefault("push.subject", cfg.Push.Subject)
	settings.SetDefault("streaming.idle_timeout_seconds", cfg.Streaming.IdleTimeoutSeconds)
	settings.SetDefault("extensions.source", cfg.Extensions.Source)
	settings.SetDefault("extensions.release", cfg.Extensions.Release)
}

func mergeEnv(settings *viper.Viper) error {
	envSettings := map[string]any{}
	if value := strings.TrimSpace(os.Getenv("KI_SERVER_ADDR")); value != "" {
		envSettings["server"] = map[string]any{"addr": value}
	}
	if value := strings.TrimSpace(os.Getenv("KI_EXTENSIONS_SOURCE")); value != "" {
		envSettings["extensions"] = map[string]any{"source": value}
	}
	if value := strings.TrimSpace(os.Getenv("KI_EXTENSIONS_RELEASE")); value != "" {
		existing, _ := envSettings["extensions"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
			envSettings["extensions"] = existing
		}
		existing["release"] = value
	}
	if len(envSettings) == 0 {
		return nil
	}
	if err := settings.MergeConfigMap(envSettings); err != nil {
		return fmt.Errorf("merge environment config: %w", err)
	}
	return nil
}

func mergeFile(settings *viper.Viper, path string) error {
	//nolint:gosec // path comes from the bounded config discovery locations.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	fileSettings := viper.New()
	fileSettings.SetConfigType("toml")
	if err := fileSettings.ReadConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := settings.MergeConfigMap(fileSettings.AllSettings()); err != nil {
		return fmt.Errorf("merge file config: %w", err)
	}
	return nil
}
