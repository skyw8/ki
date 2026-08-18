package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the merged runtime configuration.
type Config struct {
	Home       string
	Defaults   Defaults
	Providers  map[string]Provider
	Sessions   Sessions
	Compaction Compaction
	Server     Server
	Log        Log
}

// Defaults is the cross-session default model.
type Defaults struct {
	Provider string
	Model    string
}

// Provider is one model vendor's connection settings.
type Provider struct {
	APIKey  string
	BaseURL string
	// API overrides the protocol shape from the model catalog
	// ("completions" | "responses" | "anthropic"). Empty = catalog decides.
	API string
}

// Sessions holds session storage settings.
type Sessions struct {
	Root string
}

// Compaction holds auto-compaction thresholds (pi defaults).
type Compaction struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
	// MaxContextTokens caps the context window used for the threshold check
	// (min of model window and this value; 0 = model window only). A small
	// value triggers compaction early, useful for low-cost testing without
	// burning tokens on huge contexts.
	MaxContextTokens int
}

// Server is the local HTTP bind address.
type Server struct {
	Addr string
}

// Log is process log settings.
type Log struct {
	Level      string
	MaxSizeMB  int
	MaxBackups int
}

// Builtin returns compiled-in defaults.
func Builtin(home string) Config {
	if home == "" {
		home = HomeDir()
	}
	return Config{
		Home: home,
		Providers: map[string]Provider{
			"openai":       {},
			"anthropic":    {},
			"zhipu":        {},
			"zhipu-cn":     {},
			"deepseek":     {},
			"dashscope":    {},
			"dashscope-cn": {},
		},
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
	home := HomeDir()
	cfg := Builtin(home)
	global := filepath.Join(home, "ki.toml")
	if err := mergeFile(&cfg, global); err != nil {
		return Config{}, err
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd != "" {
		if err := mergeFile(&cfg, filepath.Join(cwd, ".ki", "ki.toml")); err != nil {
			return Config{}, err
		}
	}
	applyEnv(&cfg)
	if cfg.Sessions.Root == "" {
		cfg.Sessions.Root = filepath.Join(cfg.Home, "sessions")
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:19800"
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	envKeys := map[string]string{
		"OPENAI_API_KEY":        "openai",
		"ANTHROPIC_API_KEY":     "anthropic",
		"ZHIPU_API_KEY":         "zhipu",
		"ZAI_API_KEY":           "zhipu",
		"ZAI_CODING_CN_API_KEY": "zhipu-cn",
		"DEEPSEEK_API_KEY":      "deepseek",
		"DASHSCOPE_API_KEY":     "dashscope",
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	for env, id := range envKeys {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			p := cfg.Providers[id]
			p.APIKey = v
			cfg.Providers[id] = p
		}
	}
	// CN dashscope shares DASHSCOPE_API_KEY unless a dedicated one is set.
	if v := strings.TrimSpace(os.Getenv("DASHSCOPE_CN_API_KEY")); v != "" {
		p := cfg.Providers["dashscope-cn"]
		p.APIKey = v
		cfg.Providers["dashscope-cn"] = p
	} else if v := strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")); v != "" {
		p := cfg.Providers["dashscope-cn"]
		if p.APIKey == "" {
			p.APIKey = v
			cfg.Providers["dashscope-cn"] = p
		}
	}
	if v := strings.TrimSpace(os.Getenv("KI_SERVER_ADDR")); v != "" {
		cfg.Server.Addr = v
	}
}

func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return mergeTOML(cfg, string(data))
}

// mergeTOML applies a restricted TOML subset used by ki.toml.
func mergeTOML(cfg *Config, src string) error {
	section := ""
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = unquote(strings.TrimSpace(val))
		applyKey(cfg, section, key, val)
	}
	return nil
}

func applyKey(cfg *Config, section, key, val string) {
	switch section {
	case "defaults":
		switch key {
		case "provider":
			cfg.Defaults.Provider = val
		case "model":
			cfg.Defaults.Model = val
		}
	case "sessions":
		if key == "root" && val != "" {
			cfg.Sessions.Root = val
		}
	case "compaction":
		switch key {
		case "enabled":
			cfg.Compaction.Enabled = val == "true" || val == "1"
		case "reserve_tokens":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Compaction.ReserveTokens = n
			}
		case "keep_recent_tokens":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Compaction.KeepRecentTokens = n
			}
		case "max_context_tokens":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Compaction.MaxContextTokens = n
			}
		}
	case "server":
		if key == "addr" && val != "" {
			cfg.Server.Addr = val
		}
	case "log":
		switch key {
		case "level":
			if val != "" {
				cfg.Log.Level = val
			}
		case "max_size_mb":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Log.MaxSizeMB = n
			}
		case "max_backups":
			if n, err := strconv.Atoi(val); err == nil {
				cfg.Log.MaxBackups = n
			}
		}
	default:
		if id, ok := strings.CutPrefix(section, "providers."); ok {
			if cfg.Providers == nil {
				cfg.Providers = map[string]Provider{}
			}
			p := cfg.Providers[id]
			switch key {
			case "api_key":
				p.APIKey = val
			case "base_url":
				p.BaseURL = val
			case "api":
				p.API = val
			}
			cfg.Providers[id] = p
		}
	}
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// HasKey reports whether a provider has an API key after merge.
func (c Config) HasKey(id string) bool {
	p, ok := c.Providers[id]
	return ok && strings.TrimSpace(p.APIKey) != ""
}
