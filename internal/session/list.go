package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Info is a session row for listing.
type Info struct {
	ID              string         `json:"id"`
	CWD             string         `json:"cwd"`
	Dir             string         `json:"dir"`
	Provider        string         `json:"provider"`
	Model           string         `json:"model"`
	Timestamp       string         `json:"timestamp"`
	ParentSessionID string         `json:"parentSessionId,omitempty"`
	ForkMode        string         `json:"forkMode"`
	Title           string         `json:"title"`
	Pinned          bool           `json:"pinned,omitempty"`
	PinnedAt        string         `json:"pinnedAt,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// List walks the session root and returns every readable session, newest first.
//
// A list row only needs config.json, the jsonl header, and the first user
// message used as a title fallback. Parsing each session's full transcript here
// made one list request O(total history): a 12 MB session was decoded in full
// to render one sidebar line. liteInfo reads at most a few lines per session,
// so listing cost tracks the number of sessions, not the size of their history.
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
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out, nil
}

// liteInfo reads one list row without decoding transcript bodies beyond the
// first user message.
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
	return Info{
		ID:              header.ID,
		CWD:             header.CWD,
		Dir:             dir,
		Provider:        cfg.Provider,
		Model:           cfg.Model,
		Timestamp:       header.Timestamp,
		ParentSessionID: header.ParentSession,
		ForkMode:        header.EffectiveForkMode(),
		Title:           title,
		Pinned:          cfg.Pinned,
		PinnedAt:        cfg.PinnedAt,
		Metadata:        cloneMetadata(cfg.Metadata),
	}, nil
}

// maxListLineBytes matches Open's scanner so a single oversized row is never
// the reason a session disappears from the list.
const maxListLineBytes = 16 * 1024 * 1024

// scanHeaderAndTitle decodes only the header and — when config.json has no
// title — the first user message. It stops as soon as the title is known.
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
				// config.title is authoritative; never touch the entries.
				return header, title, nil
			}
			continue
		}
		var e liteEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// A row this scan cannot decode also cannot contribute a title.
			// Skip it instead of hiding the whole session from the list, which
			// a torn append at the transcript tail would otherwise cause.
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

// liteEntry is the minimal transcript shape needed for the title fallback.
// Decoding a full Entry here would reintroduce the tool args and thinking
// payloads this path exists to avoid.
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

// liteText mirrors types.Message.Text: concatenate text or untyped blocks.
func liteText(m *liteMsg) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// TitleOf prefers config.json title, else the first user message, truncated.
func TitleOf(s *Session) string {
	return TitleFrom(s.Config, s.Entries())
}

// TitleFrom prefers config.json title, else the first user message, truncated.
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

// truncateTitle clips an auto title to 80 runes, keeping the sidebar row shape
// identical whether the title came from a full Entry or the lite scan.
func truncateTitle(t string) string {
	if utf8.RuneCountInString(t) > 80 {
		return string([]rune(t)[:80]) + "…"
	}
	return t
}
