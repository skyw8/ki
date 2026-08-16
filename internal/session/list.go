package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Info is a session row for listing.
type Info struct {
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	Dir           string `json:"dir"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Timestamp     string `json:"timestamp"`
	ParentSession string `json:"parent,omitempty"`
	Title         string `json:"title"`
}

// List walks the session root and returns every readable session, newest first.
func List(root string) ([]Info, error) {
	encs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, enc := range encs {
		if !enc.IsDir() {
			continue
		}
		inner := filepath.Join(root, enc.Name())
		subs, err := os.ReadDir(inner)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() {
				continue
			}
			s, err := Open(filepath.Join(inner, sub.Name()))
			if err != nil {
				continue
			}
			out = append(out, Info{
				ID:            s.ID(),
				CWD:           s.Header.CWD,
				Dir:           s.Dir,
				Provider:      s.Config.Provider,
				Model:         s.Config.Model,
				Timestamp:     s.Header.Timestamp,
				ParentSession: s.Header.ParentSession,
				Title:         TitleOf(s),
			})
			_ = s.Close()
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out, nil
}

// TitleOf is the first user message, truncated.
func TitleOf(s *Session) string {
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
