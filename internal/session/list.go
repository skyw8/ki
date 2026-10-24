package session

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

type Info struct {
	ID                   string         `json:"id"`
	CWD                  string         `json:"cwd"`
	Dir                  string         `json:"dir"`
	Provider             string         `json:"provider"`
	Model                string         `json:"model"`
	Timestamp            string         `json:"timestamp"`
	UpdatedAt            string         `json:"updatedAt,omitempty"`
	ParentSessionID      string         `json:"parentSessionId,omitempty"`
	ForkMode             string         `json:"forkMode"`
	Title                string         `json:"title"`
	Pinned               bool           `json:"pinned,omitzero"`
	PinnedAt             string         `json:"pinnedAt,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
	QuestionLinks        []QuestionLink `json:"questionLinks,omitempty"`
	QuestionLinksLoading bool           `json:"questionLinksLoading,omitempty"`
}

func List(root string) ([]Info, error) {
	var out []Info
	err := walkSessionDirs(root, func(dir string) bool {
		info, err := liteInfo(dir)
		if err != nil {
			return true
		}
		out = append(out, info)
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(b.Timestamp, a.Timestamp) })
	return out, nil
}

func liteInfo(dir string) (Info, error) {
	gate := fileGate(dir)
	gate.RLock()
	defer gate.RUnlock()

	//nolint:gosec // dir is an internally generated session directory.
	cfgb, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return Info{}, err
	}
	var cfg Config
	if err := json.Unmarshal(cfgb, &cfg); err != nil {
		return Info{}, fmt.Errorf("decode config.json: %w", err)
	}
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = f.Close() }()
	header, title, err := scanHeaderAndTitle(f, cfg)
	if err != nil {
		return Info{}, err
	}
	if header.ID == "" {
		return Info{}, fmt.Errorf("%w: %s", errSessionHeader, dir)
	}
	updatedAt := ""
	if stat, statErr := f.Stat(); statErr == nil {
		updatedAt = stat.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return Info{
		ID:              header.ID,
		CWD:             header.CWD,
		Dir:             dir,
		Provider:        cfg.Provider,
		Model:           cfg.Model,
		Timestamp:       header.Timestamp,
		UpdatedAt:       updatedAt,
		ParentSessionID: header.ParentSession,
		ForkMode:        header.EffectiveForkMode(),
		Title:           title,
		Pinned:          cfg.Pinned,
		PinnedAt:        cfg.PinnedAt,
		Metadata:        PublicMetadata(cfg.Metadata),
	}, nil
}

const maxListLineBytes = 16 * 1024 * 1024

func scanHeaderAndTitle(r io.Reader, cfg Config) (Header, string, error) {
	title := strings.TrimSpace(cfg.Title)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxListLineBytes)
	var header Header
	haveHeader := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !haveHeader {
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return Header{}, "", fmt.Errorf("decode events.jsonl header: %w", err)
			}
			haveHeader = true
			if title != "" {
				return header, title, nil
			}
			continue
		}
		var e liteEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Type != "message" || e.Message == nil || e.Message.Role != "user" {
			continue
		}
		if t := strings.TrimSpace(liteText(e.Message)); t != "" {
			return header, truncateTitle(t), nil
		}
	}
	if err := sc.Err(); err != nil {
		return Header{}, "", err
	}
	return header, title, nil
}

type liteEntry struct {
	Type    string   `json:"type"`
	Message *liteMsg `json:"message"`
}

type liteMsg struct {
	Role    string        `json:"role"`
	Content []liteContent `json:"content"`
}

type liteContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func liteText(m *liteMsg) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func TitleOf(s *Session) string {
	return TitleFrom(s.Config, s.Entries())
}

func TitleFrom(cfg Config, entries []Entry) string {
	if t := strings.TrimSpace(cfg.Title); t != "" {
		return t
	}
	for _, e := range entries {
		if e.Type != "message" || e.Message == nil || e.Message.Role != "user" {
			continue
		}
		t := strings.TrimSpace(e.Message.Text())
		if t == "" {
			continue
		}
		return truncateTitle(t)
	}
	return ""
}

func truncateTitle(t string) string {
	if utf8.RuneCountInString(t) > 80 {
		return string([]rune(t)[:80]) + "…"
	}
	return t
}
