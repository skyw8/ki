package session

import (
	"os"
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
func List(root string) ([]Info, error) {
	var out []Info
	err := walkSessionDirs(root, func(dir string) bool {
		s, err := Open(dir)
		if err != nil {
			return true
		}
		out = append(out, Info{
			ID:              s.ID(),
			CWD:             s.Header.CWD,
			Dir:             s.Dir,
			Provider:        s.Config.Provider,
			Model:           s.Config.Model,
			Timestamp:       s.Header.Timestamp,
			ParentSessionID: s.Header.ParentSession,
			ForkMode:        s.Header.EffectiveForkMode(),
			Title:           TitleOf(s),
			Pinned:          s.Config.Pinned,
			PinnedAt:        s.Config.PinnedAt,
			Metadata:        cloneMetadata(s.Config.Metadata),
		})
		_ = s.Close()
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

// TitleOf prefers config.json title, else the first user message, truncated.
func TitleOf(s *Session) string {
	if t := strings.TrimSpace(s.Config.Title); t != "" {
		return t
	}
	for _, e := range s.Entries() {
		if e.Type != "message" || e.Message == nil || e.Message.Role != "user" {
			continue
		}
		t := strings.TrimSpace(e.Message.Text())
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > 80 {
			return string([]rune(t)[:80]) + "…"
		}
		return t
	}
	return ""
}
