package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ki/internal/idgen"
	"ki/internal/types"
)

const version = 1

// Header is the first jsonl line.
type Header struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
}

// Toggle filters discovered skills or MCP servers.
type Toggle struct {
	Only     []string `json:"only,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

// Config is session-level config.json.
type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Skills   Toggle `json:"skills"`
	MCP      Toggle `json:"mcp"`
}

// Entry is one jsonl record after the header.
type Entry struct {
	Type             string         `json:"type"`
	ID               string         `json:"id"`
	ParentID         string         `json:"parentId"`
	Timestamp        string         `json:"timestamp"`
	Message          *types.Message `json:"message,omitempty"`
	Summary          string         `json:"summary,omitempty"`
	FirstKeptEntryID string         `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int            `json:"tokensBefore,omitempty"`
	Usage            *types.Usage   `json:"usage,omitempty"`
	Details          any            `json:"details,omitempty"`
	Provider         string         `json:"provider,omitempty"`
	ModelID          string         `json:"modelId,omitempty"`
}

// Session is an open conversation directory.
type Session struct {
	mu      sync.Mutex
	Dir     string
	Header  Header
	Config  Config
	entries []Entry
	byID    map[string]Entry
	leafID  string
	jsonl   *os.File
}

// EncodeCWD matches pi: --abs-path-with-dashes--
func EncodeCWD(cwd string) string {
	cwd = filepath.Clean(cwd)
	if vol := filepath.VolumeName(cwd); vol != "" {
		cwd = cwd[len(vol):]
	}
	cwd = strings.TrimLeft(cwd, `/\`)
	cwd = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, cwd)
	return "--" + cwd + "--"
}

// Create makes a new session directory under root.
func Create(root, cwd, provider, model string) (*Session, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	id := idgen.NewV7()
	ts := idgen.FileTimestamp(time.Now())
	dir := filepath.Join(root, EncodeCWD(cwd), ts+"_"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s := &Session{
		Dir: dir,
		Header: Header{
			Type:      "session",
			Version:   version,
			ID:        id,
			Timestamp: now,
			CWD:       cwd,
		},
		Config: Config{Provider: provider, Model: model},
		byID:   map[string]Entry{},
	}
	if err := s.writeConfig(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	s.jsonl = f
	if err := s.writeLine(s.Header); err != nil {
		return nil, err
	}
	return s, nil
}

// Open loads an existing session directory.
func Open(dir string) (*Session, error) {
	s := &Session{Dir: dir, byID: map[string]Entry{}}
	cfgb, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfgb, &s.Config); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	s.jsonl = f
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			if err := json.Unmarshal([]byte(line), &s.Header); err != nil {
				return nil, err
			}
			first = false
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		s.entries = append(s.entries, e)
		s.byID[e.ID] = e
		s.leafID = e.ID
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.Header.ID == "" {
		return nil, fmt.Errorf("session header missing in %s", dir)
	}
	return s, nil
}

// Find looks up a session id under root.
func Find(root, id string) (string, error) {
	encs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("session %s not found", id)
		}
		return "", err
	}
	suffix := "_" + id
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
			name := sub.Name()
			if name == id || strings.HasSuffix(name, suffix) {
				return filepath.Join(inner, name), nil
			}
		}
	}
	return "", fmt.Errorf("session %s not found", id)
}

// Close closes the jsonl file.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jsonl != nil {
		return s.jsonl.Close()
	}
	return nil
}

// ID is the session uuid.
func (s *Session) ID() string { return s.Header.ID }

// LeafID is the current tree leaf.
func (s *Session) LeafID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafID
}

// Entries returns a copy of persisted entries.
func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// MessagesToLeaf walks parent links from leaf to root and returns messages in order.
func (s *Session) MessagesToLeaf() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messagesLocked(s.leafID)
}

func (s *Session) messagesLocked(leaf string) []types.Message {
	var rev []Entry
	id := leaf
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		e, ok := s.byID[id]
		if !ok {
			break
		}
		rev = append(rev, e)
		id = e.ParentID
	}
	// chronological root → leaf
	chrono := make([]Entry, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		chrono = append(chrono, rev[i])
	}
	compIdx := -1
	for i, e := range chrono {
		if e.Type == "compaction" {
			compIdx = i
		}
	}
	if compIdx < 0 {
		var msgs []types.Message
		for _, e := range chrono {
			if e.Type == "message" && e.Message != nil {
				msgs = append(msgs, *e.Message)
			}
		}
		return msgs
	}
	comp := chrono[compIdx]
	var msgs []types.Message
	if comp.Summary != "" {
		msgs = append(msgs, types.Message{
			Role:    "user",
			Content: []types.Content{{Type: "text", Text: "Previous conversation summary:\n" + comp.Summary}},
		})
	}
	start := compIdx + 1
	if comp.FirstKeptEntryID != "" {
		for i, e := range chrono {
			if e.ID == comp.FirstKeptEntryID {
				start = i
				break
			}
		}
	}
	for _, e := range chrono[start:] {
		if e.Type == "message" && e.Message != nil {
			msgs = append(msgs, *e.Message)
		}
	}
	return msgs
}

// AppendMessage writes a message as a child of the current leaf.
func (s *Session) AppendMessage(m types.Message) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := map[string]struct{}{}
	for id := range s.byID {
		used[id] = struct{}{}
	}
	e := Entry{
		Type:      "message",
		ID:        idgen.EntryID(used),
		ParentID:  s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   &m,
	}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// AppendModelChange records a session default model update.
func (s *Session) AppendModelChange(provider, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := map[string]struct{}{}
	for id := range s.byID {
		used[id] = struct{}{}
	}
	e := Entry{
		Type:      "model_change",
		ID:        idgen.EntryID(used),
		ParentID:  s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:  provider,
		ModelID:   model,
	}
	if err := s.appendLocked(e); err != nil {
		return err
	}
	s.Config.Provider = provider
	s.Config.Model = model
	return s.writeConfig()
}

// AppendCompaction records a compaction checkpoint.
func (s *Session) AppendCompaction(summary, firstKept string, tokensBefore int, usage *types.Usage) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := map[string]struct{}{}
	for id := range s.byID {
		used[id] = struct{}{}
	}
	e := Entry{
		Type:             "compaction",
		ID:               idgen.EntryID(used),
		ParentID:         s.leafID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		Summary:          summary,
		FirstKeptEntryID: firstKept,
		TokensBefore:     tokensBefore,
		Usage:            usage,
	}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// SetModel updates config.json and appends model_change.
func (s *Session) SetModel(provider, model string) error {
	return s.AppendModelChange(provider, model)
}

func (s *Session) appendLocked(e Entry) error {
	if err := s.writeLine(e); err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
	s.leafID = e.ID
	return nil
}

func (s *Session) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := s.jsonl.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.jsonl.Sync()
}

func (s *Session) writeConfig() error {
	b, err := json.MarshalIndent(s.Config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "config.json"), append(b, '\n'), 0o644)
}

// SaveConfig writes config.json (skills/mcp toggles).
func (s *Session) SaveConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeConfig()
}

// Fork copies the whole directory to a new session id.
func Fork(root string, src *Session) (*Session, error) {
	src.mu.Lock()
	cwd := src.Header.CWD
	parent := src.Dir
	src.mu.Unlock()
	_ = src.jsonl.Sync()

	id := idgen.NewV7()
	ts := idgen.FileTimestamp(time.Now())
	dir := filepath.Join(root, EncodeCWD(cwd), ts+"_"+id)
	if err := copyDir(src.Dir, dir); err != nil {
		return nil, err
	}
	dst, err := Open(dir)
	if err != nil {
		return nil, err
	}
	dst.Header.ID = id
	dst.Header.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	dst.Header.ParentSession = parent
	if err := rewriteHeader(dst); err != nil {
		dst.Close()
		return nil, err
	}
	return dst, nil
}

func rewriteHeader(s *Session) error {
	path := filepath.Join(s.Dir, "events.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var rest []byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		rest = append(rest, sc.Bytes()...)
		rest = append(rest, '\n')
	}
	f.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	hb, err := json.Marshal(s.Header)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := out.Write(append(hb, '\n')); err != nil {
		out.Close()
		return err
	}
	if _, err := out.Write(rest); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	s.jsonl.Close()
	nf, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	s.jsonl = nf
	_, _ = s.jsonl.Seek(0, io.SeekEnd)
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		cerr := out.Close()
		if err != nil {
			return err
		}
		return cerr
	})
}

// Allowed reports whether name is enabled by t.
func (t Toggle) Allowed(name string) bool {
	if len(t.Only) > 0 {
		for _, n := range t.Only {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, n := range t.Disabled {
		if n == name {
			return false
		}
	}
	return true
}
