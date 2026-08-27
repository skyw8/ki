package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ki/internal/idgen"
	"ki/internal/types"
)

const version = 1

const (
	ForkModeFlat = "flat"
	ForkModeTree = "tree"
)

var (
	errCWDRequired     = errors.New("cwd required")
	errSessionHeader   = errors.New("session header missing")
	errSessionNotFound = errors.New("session not found")
	errActiveLeaf      = errors.New("active leaf")
	errEntryNotFound   = errors.New("entry")
)

// Header is the first jsonl line.
type Header struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
	ForkMode      string `json:"forkMode,omitempty"`
}

// NormalizeForkMode validates a fork handling mode and supplies the default
// for ordinary forks and sessions created before the mode was persisted.
func NormalizeForkMode(mode string) (string, error) {
	if mode == "" {
		return ForkModeFlat, nil
	}
	if mode != ForkModeFlat && mode != ForkModeTree {
		return "", fmt.Errorf("forkMode must be %q or %q", ForkModeFlat, ForkModeTree)
	}
	return mode, nil
}

// EffectiveForkMode treats an absent mode in an old header as a flat session.
func (h Header) EffectiveForkMode() string {
	mode, err := NormalizeForkMode(h.ForkMode)
	if err != nil {
		return ForkModeFlat
	}
	return mode
}

// Toggle filters discovered skills or extensions.
type Toggle struct {
	Only     []string `json:"only,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

// Config is session-level config.json.
type Config struct {
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	ThinkingEffort string         `json:"thinkingEffort,omitempty"`
	ActiveLeafID   string         `json:"activeLeafId,omitempty"`
	Title          string         `json:"title,omitempty"`
	Pinned         bool           `json:"pinned,omitempty"`
	PinnedAt       string         `json:"pinnedAt,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// CreateOptions controls optional session metadata at creation time.
type CreateOptions struct {
	ThinkingEffort string
	Metadata       map[string]any
}

// Entry is one jsonl record after the header.
type Entry struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	ParentID         string          `json:"parentId"`
	Timestamp        string          `json:"timestamp"`
	Message          *types.Message  `json:"message,omitempty"`
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int             `json:"tokensBefore,omitempty"`
	Usage            *types.Usage    `json:"usage,omitempty"`
	RetainedTail     []types.Message `json:"retainedTail,omitempty"`
	Details          any             `json:"details,omitempty"`
	Sideband         bool            `json:"sideband,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	ModelID          string          `json:"modelId,omitempty"`
	ThinkingEffort   string          `json:"thinkingEffort,omitempty"`
	CatalogVersion   int             `json:"catalogVersion,omitempty"`
	UsedTokens       int             `json:"usedTokens,omitempty"`
	ContextWindow    int             `json:"contextWindow,omitempty"`
	Estimated        bool            `json:"estimated,omitempty"`
	Pricing          any             `json:"pricing,omitempty"`
	System           string          `json:"system,omitempty"`
	Tools            []ToolSchema    `json:"tools,omitempty"`
}

// ToolSchema is the model-visible tool list on a request_header entry.
type ToolSchema struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *ToolFormat    `json:"format,omitempty"`
}

// ToolFormat is the persisted grammar format of a custom tool.
type ToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

// RequestMeta pins catalog identity used to build a provider request.
type RequestMeta struct {
	Provider       string
	Model          string
	ThinkingEffort string
	CatalogVersion int
	Pricing        any
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
func Create(root, cwd, provider, model string, thinking ...string) (*Session, error) {
	opts := CreateOptions{}
	if len(thinking) > 0 {
		opts.ThinkingEffort = thinking[0]
	}
	return CreateWithOptions(root, cwd, provider, model, opts)
}

// CreateWithOptions makes a new session and persists its routing metadata.
func CreateWithOptions(root, cwd, provider, model string, opts CreateOptions) (*Session, error) {
	if cwd == "" {
		return nil, errCWDRequired
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	id, err := idgen.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate session ID: %w", err)
	}
	ts := idgen.FileTimestamp(time.Now())
	dir := filepath.Join(root, EncodeCWD(cwd), ts+"_"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cfg := Config{Provider: provider, Model: model}
	if opts.ThinkingEffort != "" {
		cfg.ThinkingEffort = opts.ThinkingEffort
	}
	if opts.Metadata != nil {
		cfg.Metadata = cloneMetadata(opts.Metadata)
	}
	s := &Session{
		Dir: dir,
		Header: Header{
			Type:      "session",
			Version:   version,
			ID:        id,
			Timestamp: now,
			CWD:       cwd,
			ForkMode:  ForkModeFlat,
		},
		Config: cfg,
		byID:   map[string]Entry{},
	}
	if err := s.writeConfig(); err != nil {
		return nil, err
	}
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	s.jsonl = f
	if err := s.writeLine(s.Header); err != nil {
		return nil, err
	}
	return s, nil
}

func cloneMetadata(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// Open loads an existing session directory.
func Open(dir string) (*Session, error) {
	s := &Session{Dir: dir, byID: map[string]Entry{}}
	//nolint:gosec // dir is an internally generated session directory.
	cfgb, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfgb, &s.Config); err != nil {
		return nil, err
	}
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_RDWR|os.O_APPEND, 0o600)
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
		if !e.Sideband {
			s.leafID = e.ID
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.Header.ID == "" {
		return nil, fmt.Errorf("%w: %s", errSessionHeader, dir)
	}
	if s.Config.ActiveLeafID != "" {
		if _, ok := s.byID[s.Config.ActiveLeafID]; ok {
			s.leafID = s.Config.ActiveLeafID
		}
		// If config points at a leaf not yet visible in this jsonl snapshot
		// (concurrent append), keep the last non-sideband entry already scanned.
	}
	return s, nil
}

// Find looks up a session id under root.
func Find(root, id string) (string, error) {
	suffix := "_" + id
	found := ""
	err := walkSessionDirs(root, func(dir string) bool {
		name := filepath.Base(dir)
		if name == id || strings.HasSuffix(name, suffix) {
			found = dir
			return false
		}
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", errSessionNotFound, id)
		}
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%w: %s", errSessionNotFound, id)
	}
	return found, nil
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

// LeafEntries returns leaf-chain entries in chronological order (root → leaf),
// for compaction preparation (pure-function input, no session dependency).
func (s *Session) LeafEntries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafEntriesLocked(s.leafID)
}

// EntriesTo returns the root-to-leaf entry path for an entry in this session.
func (s *Session) EntriesTo(leaf string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf != "" {
		if _, ok := s.byID[leaf]; !ok {
			return nil, fmt.Errorf("%w %q not found", errEntryNotFound, leaf)
		}
	}
	return slices.Clone(s.leafEntriesLocked(leaf)), nil
}

// SetLeaf selects the active branch without rewriting or deleting old rows.
// Persisting it is required because the server opens a fresh Session for each
// request; an in-memory-only revert would silently jump back to the last
// physical jsonl row on the next request.
func (s *Session) SetLeaf(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		if _, ok := s.byID[id]; !ok {
			return fmt.Errorf("%w %q not found", errEntryNotFound, id)
		}
	}
	s.leafID = id
	s.Config.ActiveLeafID = id
	return s.writeConfig()
}

func (s *Session) leafEntriesLocked(leaf string) []Entry {
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
	slices.Reverse(rev)
	return rev
}

// LastCompactionAt returns the unix-ms timestamp of the most recent compaction
// entry, or 0 when none exists. Used for the stale-usage guard: usage from a
// message older than the last compaction reflects the pre-compaction (larger)
// context and would falsely trigger compaction right after one finished.
func (s *Session) LastCompactionAt() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Compactions on abandoned edit branches must not affect stale-usage checks
	// for the selected branch.
	for _, v := range slices.Backward(s.leafEntriesLocked(s.leafID)) {
		if v.Type != "compaction" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v.Timestamp)
		if err != nil {
			return 0
		}
		return t.UnixMilli()
	}
	return 0
}

// MessagesToLeaf walks parent links from leaf to root and returns messages in order.
func (s *Session) MessagesToLeaf() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messagesLocked(s.leafID)
}

// MessagesTo returns model-facing history for an arbitrary branch point.
func (s *Session) MessagesTo(leaf string) ([]types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf != "" {
		if _, ok := s.byID[leaf]; !ok {
			return nil, fmt.Errorf("%w %q not found", errEntryNotFound, leaf)
		}
	}
	return s.messagesLocked(leaf), nil
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
	// chronological root → leaf (rev is dead after this point, reverse in place)
	slices.Reverse(rev)
	chrono := rev
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
	// New entries carry the verbatim retained tail; old jsonl without it falls
	// back to slicing chrono from FirstKeptEntryID. In both cases entries AFTER
	// the compaction (messages appended since it ran) must still be included.
	if len(comp.RetainedTail) > 0 {
		msgs = append(msgs, comp.RetainedTail...)
		start := compIdx + 1
		for _, e := range chrono[start:] {
			if e.Type == "message" && e.Message != nil {
				msgs = append(msgs, *e.Message)
			}
		}
		return msgs
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
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:      "message",
		ID:        id,
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
func (s *Session) AppendModelChange(provider, model string, efforts ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:      "model_change",
		ID:        id,
		ParentID:  s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:  provider,
		ModelID:   model,
	}
	if len(efforts) > 0 {
		e.ThinkingEffort = efforts[0]
	}
	if err := s.appendLocked(e); err != nil {
		return err
	}
	s.Config.Provider = provider
	s.Config.Model = model
	if len(efforts) > 0 {
		s.Config.ThinkingEffort = efforts[0]
	}
	return s.writeConfig()
}

// AppendCompaction records a compaction checkpoint. retainedTail is the
// recent messages kept verbatim after the cut (pi retainedTail); old entries
// without it fall back to FirstKeptEntryID slicing in MessagesToLeaf.
func (s *Session) AppendCompaction(summary, firstKept string, tokensBefore int, usage *types.Usage, retainedTail []types.Message) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:             "compaction",
		ID:               id,
		ParentID:         s.leafID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		Summary:          summary,
		FirstKeptEntryID: firstKept,
		TokensBefore:     tokensBefore,
		Usage:            usage,
		RetainedTail:     retainedTail,
	}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// AppendEvent records a non-message event entry (compaction_start/end) so
// replay of the session shows when compaction happened. MessagesToLeaf ignores
// these types.
func (s *Session) AppendEvent(typ, reason string, ok bool) (Entry, error) {
	return s.AppendDetailsEvent(typ, map[string]any{"reason": reason, "ok": ok})
}

// AppendDetailsEvent records a structured non-message event. It keeps streamed
// tool previews on the same jsonl trajectory as the SSE event without turning
// them into model-facing messages.
func (s *Session) AppendDetailsEvent(typ string, details any) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:      typ,
		ID:        id,
		ParentID:  s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Details:   details,
	}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// AppendSidebandEvent persists an asynchronous event without advancing the
// conversation leaf. Extension notifications can arrive while no Session object is
// open, and making them parents of model messages would corrupt the trajectory.
func AppendSidebandEvent(dir, typ string, details any) (Entry, error) {
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{Type: typ, ID: id, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Details: details, Sideband: true}
	b, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	// O_APPEND is required because a live prompt may hold another descriptor.
	// Without it an idle SDK notification could overwrite that writer's tail.
	//nolint:gosec // dir comes from the server's indexed session record.
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return Entry{}, err
	}
	if err := f.Sync(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// AppendCustomEntry writes an extension custom row that does not enter provider context.
func AppendCustomEntry(dir, extensionName, customType string, data any) (Entry, error) {
	details := map[string]any{"extension": extensionName, "customType": customType, "data": data}
	return AppendSidebandEvent(dir, "custom", details)
}

// CustomEntries returns custom jsonl rows for one extension (own data only).
func CustomEntries(dir, extensionName string) []map[string]any {
	s, err := Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = s.Close() }()
	var out []map[string]any
	for _, e := range s.Entries() {
		if e.Type != "custom" {
			continue
		}
		m, _ := e.Details.(map[string]any)
		if m == nil {
			continue
		}
		if fmt.Sprint(m["extension"]) != extensionName {
			continue
		}
		out = append(out, m)
	}
	return out
}

// AppendRequestHeader records the system prompt and tools sent on one turn.
func (s *Session) AppendRequestHeader(system string, tools []ToolSchema, metadata ...RequestMeta) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:      "request_header",
		ID:        id,
		ParentID:  s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		System:    system,
		Tools:     tools,
	}
	if len(metadata) > 0 {
		e.Provider = metadata[0].Provider
		e.ModelID = metadata[0].Model
		e.ThinkingEffort = metadata[0].ThinkingEffort
		e.CatalogVersion = metadata[0].CatalogVersion
		e.Pricing = metadata[0].Pricing
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

// SetModelAndThinking updates the selected model and thinking effort together.
func (s *Session) SetModelAndThinking(provider, model, effort string) error {
	return s.AppendModelChange(provider, model, effort)
}

// AppendContextUsage records the model-facing context size for the session.
func (s *Session) AppendContextUsage(used, window int, estimated bool) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("allocate context usage entry id: %w", err)
	}
	e := Entry{Type: "context_usage", ID: id, ParentID: s.leafID, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), UsedTokens: used, ContextWindow: window, Estimated: estimated}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// SetTitle writes a pinned display title (empty clears the override).
func (s *Session) SetTitle(title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Config.Title = strings.TrimSpace(title)
	return s.writeConfig()
}

// SetPinned writes the pin flag. pinnedAt is stamped on the first pin.
func (s *Session) SetPinned(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.Config.Pinned = true
		if s.Config.PinnedAt == "" {
			s.Config.PinnedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	} else {
		s.Config.Pinned = false
		s.Config.PinnedAt = ""
	}
	return s.writeConfig()
}

// Remove closes and deletes a session directory.
func Remove(dir string) error {
	return os.RemoveAll(dir)
}

func (s *Session) appendLocked(e Entry) error {
	if err := s.writeLine(e); err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
	s.leafID = e.ID
	s.Config.ActiveLeafID = e.ID
	return s.writeConfig()
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
	return os.WriteFile(filepath.Join(s.Dir, "config.json"), append(b, '\n'), 0o600)
}

// SaveConfig writes config.json.
func (s *Session) SaveConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeConfig()
}

// Fork creates a new session containing the current active path.
func Fork(root string, src *Session) (*Session, error) {
	return ForkAt(root, src, src.LeafID())
}

// ForkAt creates a new session directory containing only root -> target.
func ForkAt(root string, src *Session, target string, requestedMode ...string) (*Session, error) {
	forkMode := ForkModeFlat
	if len(requestedMode) > 0 {
		var err error
		forkMode, err = NormalizeForkMode(requestedMode[0])
		if err != nil {
			return nil, err
		}
	}
	src.mu.Lock()
	cwd := src.Header.CWD
	parent := src.Header.ID
	cfg := src.Config
	if target == "" {
		target = src.leafID
	}
	if target != "" {
		if _, ok := src.byID[target]; !ok {
			src.mu.Unlock()
			return nil, fmt.Errorf("%w %q not found", errEntryNotFound, target)
		}
	}
	entries := slices.Clone(src.leafEntriesLocked(target))
	sourceDir := src.Dir
	src.mu.Unlock()

	dst, err := Create(root, cwd, cfg.Provider, cfg.Model, cfg.ThinkingEffort)
	if err != nil {
		return nil, err
	}
	dir := dst.Dir
	attachmentRefs := referencedAttachments(entries, sourceDir)
	for i := range entries {
		rewriteEntryAttachmentPaths(&entries[i], sourceDir, dir)
	}
	fail := func(err error) (*Session, error) {
		_ = dst.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	dst.Config = cfg
	dst.Config.ActiveLeafID = ""
	dst.Header.ParentSession = parent
	dst.Header.ForkMode = forkMode
	if err := dst.writeConfig(); err != nil {
		return fail(err)
	}
	if err := rewriteHeader(dst); err != nil {
		return fail(err)
	}
	if err := dst.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	dst, err = Open(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	for _, e := range entries {
		dst.mu.Lock()
		err = dst.appendLocked(e)
		dst.mu.Unlock()
		if err != nil {
			return fail(err)
		}
	}
	if len(attachmentRefs) > 0 {
		if err := copyAttachmentRefs(filepath.Join(sourceDir, "attachments"), filepath.Join(dir, "attachments"), attachmentRefs); err != nil {
			return fail(err)
		}
	}
	return dst, nil
}

func referencedAttachments(entries []Entry, sourceDir string) []string {
	set := map[string]bool{}
	add := func(m *types.Message) {
		if m == nil {
			return
		}
		for _, c := range m.Content {
			rel, err := filepath.Rel(filepath.Join(sourceDir, "attachments"), c.Path)
			if c.Path != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				set[rel] = true
			}
		}
	}
	for i := range entries {
		add(entries[i].Message)
		for j := range entries[i].RetainedTail {
			add(&entries[i].RetainedTail[j])
		}
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

func copyAttachmentRefs(src, dst string, refs []string) error {
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, rel := range refs {
		in, err := root.Open(rel)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = in.Close()
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // destination is confined to a generated session dir
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		_ = in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func rewriteEntryAttachmentPaths(e *Entry, sourceDir, destDir string) {
	rewrite := func(m *types.Message) {
		if m == nil {
			return
		}
		m.Content = slices.Clone(m.Content)
		for i := range m.Content {
			path := m.Content[i].Path
			if path == "" {
				continue
			}
			rel, err := filepath.Rel(filepath.Join(sourceDir, "attachments"), path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			m.Content[i].Path = filepath.Join(destDir, "attachments", rel)
		}
	}
	if e.Message != nil {
		copyMessage := *e.Message
		e.Message = &copyMessage
		rewrite(e.Message)
	}
	e.RetainedTail = slices.Clone(e.RetainedTail)
	for i := range e.RetainedTail {
		rewrite(&e.RetainedTail[i])
	}
}

func rewriteHeader(s *Session) error {
	path := filepath.Join(s.Dir, "events.jsonl")
	//nolint:gosec // path is the session file selected by the session index.
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
	_ = f.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	//nolint:gosec // path is the session file selected by the session index.
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	hb, err := json.Marshal(s.Header)
	if err != nil {
		_ = out.Close()
		return err
	}
	if _, err := out.Write(append(hb, '\n')); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := out.Write(rest); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = s.jsonl.Close()
	//nolint:gosec // path is the session file selected by the session index.
	nf, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	s.jsonl = nf
	_, _ = s.jsonl.Seek(0, io.SeekEnd)
	return nil
}

// Allowed reports whether name is enabled by t.
func (t Toggle) Allowed(name string) bool {
	if len(t.Only) > 0 && !slices.Contains(t.Only, name) {
		return false
	}
	return !slices.Contains(t.Disabled, name)
}
