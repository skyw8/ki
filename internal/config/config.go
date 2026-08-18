package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config is the merged runtime configuration.
type Config struct {
	Home       string     `mapstructure:"-"`
	Sessions   Sessions   `mapstructure:"sessions"`
	Compaction Compaction `mapstructure:"compaction"`
	Server     Server     `mapstructure:"server"`
	Log        Log        `mapstructure:"log"`
}

// Sessions holds session storage settings.
type Sessions struct {
	Root string `mapstructure:"root"`
}

// Compaction holds auto-compaction thresholds (pi defaults).
type Compaction struct {
	Enabled          bool `mapstructure:"enabled"`
	ReserveTokens    int  `mapstructure:"reserve_tokens"`
	KeepRecentTokens int  `mapstructure:"keep_recent_tokens"`
	// MaxContextTokens caps the context window used for the threshold check
	// (min of model window and this value; 0 = model window only). A small
	// value triggers compaction early, useful for low-cost testing without
	// burning tokens on huge contexts.
	MaxContextTokens int `mapstructure:"max_context_tokens"`
}

// Server is the local HTTP bind address.
type Server struct {
	Addr string `mapstructure:"addr"`
}

// Log is process log settings.
type Log struct {
	Level      string `mapstructure:"level"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
}

// Builtin returns compiled-in defaults.
func Builtin(home string) Config {
	if home == "" {
		home = HomeDir()
	}
	return Config{
		Home: home,
		Sessions: Sessions{
			Root: filepath.Join(home, "sessions"),
		},
		Compaction: Compaction{
			Enabled:          true,
			ReserveTokens:    16384,
			KeepRecentTokens: 20000,
		},
		Server: Server{Addr: "127.0.0.1:19800"},
		Log:    Log{Level: "info", MaxSizeMB: 10, MaxBackups: 3},
	}
}

// HomeDir is KI_HOME or ~/.ki.
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

// Load merges builtin ← global ki.toml ← project ki.toml ← env keys.
func Load(cwd string) (Config, error) {
	return LoadWithViper(cwd, nil)
}

// LoadWithViper is Load with an optional Viper instance supplied by the CLI.
// A caller can bind Cobra/pflag values to this instance before loading so
// command-line overrides participate in the same final decode.
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
	settings.SetDefault("sessions.root", cfg.Sessions.Root)
	settings.SetDefault("compaction.enabled", cfg.Compaction.Enabled)
	settings.SetDefault("compaction.reserve_tokens", cfg.Compaction.ReserveTokens)
	settings.SetDefault("compaction.keep_recent_tokens", cfg.Compaction.KeepRecentTokens)
	settings.SetDefault("compaction.max_context_tokens", cfg.Compaction.MaxContextTokens)
	settings.SetDefault("server.addr", cfg.Server.Addr)
	settings.SetDefault("log.level", cfg.Log.Level)
	settings.SetDefault("log.max_size_mb", cfg.Log.MaxSizeMB)
	settings.SetDefault("log.max_backups", cfg.Log.MaxBackups)
}

func mergeEnv(settings *viper.Viper) error {
	envSettings := map[string]any{}
	if value := strings.TrimSpace(os.Getenv("KI_SERVER_ADDR")); value != "" {
		envSettings["server"] = map[string]any{"addr": value}
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
	// MergeConfigMap recursively overlays tables; this preserves provider
	// fields from the global file when the project file only changes one key.
	if err := settings.MergeConfigMap(fileSettings.AllSettings()); err != nil {
		return fmt.Errorf("merge file config: %w", err)
	}
	return nil
}
