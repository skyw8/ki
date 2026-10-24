package session

import (
	"bufio"
	"context"
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

const maxJSONLLineBytes = 64 * 1024 * 1024

const (
	ForkModeFlat = "flat"
	ForkModeTree = "tree"
)

var (
	errCWDRequired     = errors.New("cwd required")
	errSessionHeader   = errors.New("session header missing")
	ErrSessionNotFound = errors.New("session not found")
	errEntryNotFound   = errors.New("entry")
	errInvalidForkMode = errors.New("invalid forkMode")
	errSessionChanged  = errors.New("session changed after compaction preparation")
	fileGates          sync.Map
)

func fileGate(dir string) *sync.RWMutex {
	key := filepath.Clean(dir)
	gate, _ := fileGates.LoadOrStore(key, &sync.RWMutex{})
	mu, ok := gate.(*sync.RWMutex)
	if !ok {
		mu = &sync.RWMutex{}
		fileGates.Store(key, mu)
	}
	return mu
}

type Header struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
	ForkMode      string `json:"forkMode,omitempty"`
}

func NormalizeForkMode(mode string) (string, error) {
	if mode == "" {
		return ForkModeFlat, nil
	}
	if mode != ForkModeFlat && mode != ForkModeTree {
		return "", fmt.Errorf("%w: must be %q or %q", errInvalidForkMode, ForkModeFlat, ForkModeTree)
	}
	return mode, nil
}

func (h Header) EffectiveForkMode() string {
	mode, err := NormalizeForkMode(h.ForkMode)
	if err != nil {
		return ForkModeFlat
	}
	return mode
}

type Toggle struct {
	Only     []string `json:"only,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

type Config struct {
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	ThinkingEffort string         `json:"thinkingEffort,omitempty"`
	ActiveLeafID   string         `json:"activeLeafId,omitempty"`
	Title          string         `json:"title,omitempty"`
	Pinned         bool           `json:"pinned,omitzero"`
	PinnedAt       string         `json:"pinnedAt,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CacheKey       string         `json:"cacheKey,omitempty"`
}

func (s *Session) CacheAffinityKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Config.CacheKey != "" {
		return s.Config.CacheKey
	}
	return s.Header.ID
}

type CreateOptions struct {
	ThinkingEffort string
	Metadata       map[string]any
}

type Entry struct {
	Type             string                  `json:"type"`
	ID               string                  `json:"id"`
	ParentID         string                  `json:"parentId"`
	Timestamp        string                  `json:"timestamp"`
	IdempotencyKey   string                  `json:"idempotencyKey,omitempty"`
	Message          *types.Message          `json:"message,omitempty"`
	Summary          string                  `json:"summary,omitempty"`
	FirstKeptEntryID string                  `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int                     `json:"tokensBefore,omitzero"`
	Usage            *types.Usage            `json:"usage,omitempty"`
	RetainedTail     []types.Message         `json:"retainedTail,omitempty"`
	Responses        *types.ResponsesContext `json:"responses,omitempty"`
	Details          any                     `json:"details,omitempty"`
	Sideband         bool                    `json:"sideband,omitzero"`
	Provider         string                  `json:"provider,omitempty"`
	ModelID          string                  `json:"modelId,omitempty"`
	ThinkingEffort   string                  `json:"thinkingEffort,omitempty"`
	CatalogVersion   int                     `json:"catalogVersion,omitzero"`
	UsedTokens       int                     `json:"usedTokens,omitzero"`
	ContextWindow    int                     `json:"contextWindow,omitzero"`
	Estimated        bool                    `json:"estimated,omitzero"`
	Pricing          any                     `json:"pricing,omitempty"`
	System           string                  `json:"system,omitempty"`
	Tools            []ToolSchema            `json:"tools,omitempty"`
	RemoteContext    bool                    `json:"remoteContext,omitempty"`
	PromptUnchanged  bool                    `json:"promptUnchanged,omitzero"`
	Truncated        bool                    `json:"truncated,omitzero"`
	ContextEstimate  *ContextEstimate        `json:"contextEstimate,omitempty"`
	CacheMiss        *types.CacheMiss        `json:"cacheMiss,omitempty"`
}

type ToolSchema struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *ToolFormat    `json:"format,omitempty"`
}

type ToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

type RequestMeta struct {
	Provider       string
	Model          string
	ThinkingEffort string
	CatalogVersion int
	Pricing        any
}

type Session struct {
	mu      sync.Mutex
	Dir     string
	Header  Header
	Config  Config
	entries []Entry
	prompts promptPool
	byID    map[string]Entry
	leafID  string
}

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

func Create(root, cwd, provider, model string, thinking ...string) (*Session, error) {
	opts := CreateOptions{}
	if len(thinking) > 0 {
		opts.ThinkingEffort = thinking[0]
	}
	return CreateWithOptions(root, cwd, provider, model, opts)
}

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
	if err := s.writeLine(s.Header); err != nil {
		return nil, err
	}
	return s, nil
}

func PublicMetadata(in map[string]any) map[string]any {
	if in["purpose"] != "question_side" {
		return cloneMetadata(in)
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch key {
		case "questionSideResources", "readOnlyWorkspaceRoot", "readOnlyAttachmentsRoot", "readOnlyProfile", "sideInitialization":
			continue
		}
		out[key] = value
	}
	return cloneMetadata(out)
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

func Open(dir string) (*Session, error) {
	gate := fileGate(dir)
	gate.RLock()
	defer gate.RUnlock()
	s := &Session{Dir: dir, byID: map[string]Entry{}}
	//nolint:gosec // dir is an internally generated session directory.
	cfgb, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfgb, &s.Config); err != nil {
		return nil, fmt.Errorf("decode config.json: %w", err)
	}
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			if err := json.Unmarshal([]byte(line), &s.Header); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("decode events.jsonl header: %w", err)
			}
			first = false
			continue
		}
		e, err := decodeEntry([]byte(line), &s.prompts)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("decode events.jsonl entry: %w", err)
		}
		s.entries = append(s.entries, e)
		s.byID[e.ID] = e
		if !e.Sideband {
			s.leafID = e.ID
		}
	}
	if err := sc.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if s.Header.ID == "" {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", errSessionHeader, dir)
	}
	if s.Config.ActiveLeafID != "" {
		if _, ok := s.byID[s.Config.ActiveLeafID]; ok {
			s.leafID = s.Config.ActiveLeafID
		}
	}
	return s, nil
}

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
			return "", fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return found, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return nil
}

func (s *Session) ID() string { return s.Header.ID }

func (s *Session) LeafID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafID
}

func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *Session) Lookup(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	return e, ok
}

func (s *Session) LeafEntries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafEntriesLocked(s.leafID)
}

func (s *Session) LastRequestHeader() (string, []ToolSchema, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range slices.Backward(s.leafEntriesLocked(s.leafID)) {
		if v.Type == "request_header" {
			return v.System, v.Tools, true
		}
	}
	return "", nil, false
}

func (s *Session) LastUserBoundary() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.leafID
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		e, ok := s.byID[id]
		if !ok {
			break
		}
		if e.Type == "message" && e.Message != nil && e.Message.Role == "user" && !e.Message.ContextOnly {
			if e.ParentID == "" {
				return "", false
			}
			return e.ParentID, true
		}
		id = e.ParentID
	}
	return "", false
}

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

func (s *Session) LastCompactionAt() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *Session) MessagesToLeaf() []types.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messagesLocked(s.leafID)
}

func (s *Session) ContextToLeaf(target types.ProviderBinding) types.ModelContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextLocked(s.leafID, target)
}

func (s *Session) ContextTo(leaf string, target types.ProviderBinding) (types.ModelContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf != "" {
		if _, ok := s.byID[leaf]; !ok {
			return types.ModelContext{}, fmt.Errorf("%w %q not found", errEntryNotFound, leaf)
		}
	}
	return s.contextLocked(leaf, target), nil
}

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
	slices.Reverse(rev)
	chrono := rev
	compIdx := -1
	for i, e := range chrono {
		if e.Type == "compaction" && e.Responses == nil {
			compIdx = i
		}
	}
	if compIdx < 0 {
		return projectToolExchanges(chrono, nil)
	}
	comp := chrono[compIdx]
	var msgs []types.Message
	if comp.Summary != "" {
		msgs = append(msgs, types.Message{
			Role:    "user",
			Content: []types.Content{{Type: "text", Text: "Previous conversation summary:\n" + comp.Summary}},
		})
	}
	if len(comp.RetainedTail) > 0 {
		msgs = append(msgs, comp.RetainedTail...)
		return projectToolExchanges(chrono[compIdx+1:], msgs)
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
	return projectToolExchanges(chrono[start:], msgs)
}

func (s *Session) contextLocked(leaf string, target types.ProviderBinding) types.ModelContext {
	chrono := s.leafEntriesLocked(leaf)
	latestCompaction := -1
	for i, e := range chrono {
		if e.Type == "compaction" {
			latestCompaction = i
		}
	}
	if latestCompaction >= 0 {
		comp := chrono[latestCompaction]
		if comp.Responses != nil && comp.Responses.Binding.Equal(target) {
			ctx := cloneResponsesContext(comp.Responses)
			return types.ModelContext{Responses: ctx, Messages: projectToolExchanges(chrono[latestCompaction+1:], nil)}
		}
		if comp.Responses != nil {
			return types.ModelContext{Messages: s.messagesLocked(leaf), Portable: true}
		}
	}
	return types.ModelContext{Messages: s.messagesLocked(leaf)}
}

func projectToolExchanges(entries []Entry, prefix []types.Message) []types.Message {
	messages := slices.Clone(prefix)
	results := map[string]bool{}
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "toolResult" {
			results[e.Message.ToolCallID] = true
		}
	}
	pending := unresolvedToolCalls(prefix)
	closeInterrupted := func() {
		var remaining []types.Content
		for _, call := range pending {
			if results[call.ID] {
				remaining = append(remaining, call)
				continue
			}
			messages = append(messages, interruptedToolResult(call))
		}
		pending = remaining
	}
	for _, e := range entries {
		if e.Type == "run_aborted" || e.Type == "request_header" ||
			(e.Type == "message" && e.Message != nil && e.Message.Role == "assistant") {
			closeInterrupted()
		}
		if e.Type != "message" || e.Message == nil {
			continue
		}
		m := *e.Message
		messages = append(messages, m)
		pending = append(pending, m.ToolCalls()...)
		if m.Role == "toolResult" {
			pending = slices.DeleteFunc(pending, func(call types.Content) bool { return call.ID == m.ToolCallID })
		}
	}
	return messages
}

func unresolvedToolCalls(messages []types.Message) []types.Content {
	var pending []types.Content
	for _, m := range messages {
		for _, call := range m.ToolCalls() {
			if call.ID != "" {
				pending = append(pending, call)
			}
		}
		if m.Role == "toolResult" {
			pending = slices.DeleteFunc(pending, func(call types.Content) bool { return call.ID == m.ToolCallID })
		}
	}
	return pending
}

func interruptedToolResult(call types.Content) types.Message {
	return types.Message{
		Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name, ToolType: call.ToolType,
		IsError: true,
		Content: []types.Content{{Type: "text", Text: "Tool execution was interrupted before a result was recorded. Its outcome is unknown; verify any side effects before retrying."}},
	}
}

func (s *Session) IsInterruptedToolTail(leaf string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf != s.leafID {
		return false
	}
	pending := unresolvedToolCalls(s.messagesLocked(leaf))
	if len(pending) == 0 {
		return false
	}
	ids := make(map[string]bool, len(pending))
	for _, call := range pending {
		ids[call.ID] = true
	}
	for _, e := range s.entries {
		if e.Message != nil && e.Message.Role == "toolResult" && ids[e.Message.ToolCallID] {
			return false
		}
	}
	return true
}

func (s *Session) RecoverInterruptedToolCalls() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := unresolvedToolCalls(s.messagesLocked(s.leafID))
	var recovered []Entry
	for _, call := range pending {
		id, err := idgen.EntryID()
		if err != nil {
			return recovered, fmt.Errorf("generate entry ID: %w", err)
		}
		m := interruptedToolResult(call)
		now := time.Now().UTC()
		m.Timestamp = now.UnixMilli()
		e := Entry{Type: "message", ID: id, ParentID: s.leafID, Timestamp: now.Format(time.RFC3339Nano), Message: &m}
		if err := s.appendLocked(e); err != nil {
			return recovered, err
		}
		recovered = append(recovered, e)
	}
	return recovered, nil
}

func cloneResponsesContext(in *types.ResponsesContext) *types.ResponsesContext {
	if in == nil {
		return nil
	}
	out := &types.ResponsesContext{Binding: in.Binding, Items: make([]json.RawMessage, len(in.Items))}
	for i, item := range in.Items {
		out.Items[i] = slices.Clone(item)
	}
	return out
}

func (s *Session) AppendMessage(m types.Message) (Entry, error) {
	e, _, err := s.AppendMessageWithKey(m, "")
	return e, err
}

func (s *Session) AppendMessageWithKey(m types.Message, key string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		for _, existing := range s.entries {
			if existing.Type == "message" && existing.IdempotencyKey == key {
				return existing, true, nil
			}
		}
	}
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, false, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:           "message",
		ID:             id,
		ParentID:       s.leafID,
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		IdempotencyKey: key,
		Message:        &m,
	}
	if err := s.appendLocked(e); err != nil {
		return Entry{}, false, err
	}
	return e, false, nil
}

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

func (s *Session) AppendCompaction(summary, firstKept string, tokensBefore int, usage *types.Usage, retainedTail []types.Message) (Entry, error) {
	return s.appendCompaction("", false, summary, firstKept, tokensBefore, usage, retainedTail, nil)
}

func (s *Session) AppendPreparedCompaction(expectedLeaf, summary, firstKept string, tokensBefore int, usage *types.Usage, retainedTail []types.Message, details any) (Entry, error) {
	return s.appendCompaction(expectedLeaf, true, summary, firstKept, tokensBefore, usage, retainedTail, details)
}

func (s *Session) appendCompaction(expectedLeaf string, prepared bool, summary, firstKept string, tokensBefore int, usage *types.Usage, retainedTail []types.Message, details any) (Entry, error) {
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
		Details:          details,
	}
	if prepared {
		e.ParentID = expectedLeaf
		err = s.appendPreparedLocked(e, expectedLeaf)
	} else {
		err = s.appendLocked(e)
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *Session) AppendResponsesCompaction(checkpoint types.ResponsesContext, tokensBefore int, usage *types.Usage) (Entry, error) {
	return s.appendResponsesCompaction("", false, checkpoint, tokensBefore, usage)
}

func (s *Session) AppendPreparedResponsesCompaction(expectedLeaf string, checkpoint types.ResponsesContext, tokensBefore int, usage *types.Usage) (Entry, error) {
	return s.appendResponsesCompaction(expectedLeaf, true, checkpoint, tokensBefore, usage)
}

func (s *Session) appendResponsesCompaction(expectedLeaf string, prepared bool, checkpoint types.ResponsesContext, tokensBefore int, usage *types.Usage) (Entry, error) {
	copyCheckpoint, err := validatedResponsesContext(checkpoint)
	if err != nil {
		return Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, fmt.Errorf("generate entry ID: %w", err)
	}
	e := Entry{
		Type:         "compaction",
		ID:           id,
		ParentID:     s.leafID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		TokensBefore: tokensBefore,
		Usage:        usage,
		Responses:    copyCheckpoint,
	}
	if prepared {
		e.ParentID = expectedLeaf
	}
	if err := validateJSONLLine(e); err != nil {
		return Entry{}, err
	}
	if prepared {
		err = s.appendPreparedLocked(e, expectedLeaf)
	} else {
		err = s.appendLocked(e)
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *Session) AppendMessageAndResponsesCompaction(
	message types.Message,
	checkpoint types.ResponsesContext,
	tokensBefore int,
) (Entry, Entry, error) {
	copyCheckpoint, err := validatedResponsesContext(checkpoint)
	if err != nil {
		return Entry{}, Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID, err := idgen.EntryID()
	if err != nil {
		return Entry{}, Entry{}, fmt.Errorf("generate message entry ID: %w", err)
	}
	checkpointID, err := idgen.EntryID()
	if err != nil {
		return Entry{}, Entry{}, fmt.Errorf("generate compaction entry ID: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	messageEntry := Entry{
		Type: "message", ID: messageID, ParentID: s.leafID, Timestamp: now, Message: &message,
	}
	checkpointEntry := Entry{
		Type: "compaction", ID: checkpointID, ParentID: messageID, Timestamp: now,
		TokensBefore: tokensBefore, Responses: copyCheckpoint,
	}
	messageJSON, err := json.Marshal(messageEntry)
	if err != nil {
		return Entry{}, Entry{}, err
	}
	checkpointJSON, err := json.Marshal(checkpointEntry)
	if err != nil {
		return Entry{}, Entry{}, err
	}
	if len(messageJSON) >= maxJSONLLineBytes || len(checkpointJSON) >= maxJSONLLineBytes {
		return Entry{}, Entry{}, errors.New("responses compaction entry is too large")
	}
	batch := make([]byte, 0, len(messageJSON)+len(checkpointJSON)+2)
	batch = append(batch, messageJSON...)
	batch = append(batch, '\n')
	batch = append(batch, checkpointJSON...)
	batch = append(batch, '\n')
	nextConfig, err := s.appendRawAndConfigAtLeaf(batch, s.leafID, checkpointID)
	if err != nil {
		return Entry{}, Entry{}, err
	}
	s.entries = append(s.entries, messageEntry, checkpointEntry)
	s.byID[messageID], s.byID[checkpointID] = messageEntry, checkpointEntry
	s.leafID = checkpointID
	s.Config = nextConfig
	return messageEntry, checkpointEntry, nil
}

func validatedResponsesContext(checkpoint types.ResponsesContext) (*types.ResponsesContext, error) {
	if strings.TrimSpace(checkpoint.Binding.Provider) == "" ||
		strings.TrimSpace(checkpoint.Binding.API) == "" ||
		strings.TrimSpace(checkpoint.Binding.BaseURL) == "" ||
		strings.TrimSpace(checkpoint.Binding.Model) == "" {
		return nil, errors.New("invalid responses compaction binding")
	}
	if len(checkpoint.Items) == 0 {
		return nil, errors.New("responses compaction output is empty")
	}
	copyCheckpoint := cloneResponsesContext(&checkpoint)
	total := 0
	hasCompaction := false
	for _, raw := range copyCheckpoint.Items {
		total += len(raw)
		if total > maxJSONLLineBytes {
			return nil, errors.New("responses compaction output is too large")
		}
		var item map[string]any
		if len(raw) == 0 || json.Unmarshal(raw, &item) != nil || item == nil {
			return nil, errors.New("responses compaction item must be a JSON object")
		}
		if typ, _ := item["type"].(string); typ == "compaction" {
			encrypted, _ := item["encrypted_content"].(string)
			hasCompaction = hasCompaction || encrypted != ""
		}
	}
	if !hasCompaction {
		return nil, errors.New("responses compaction output has no compaction item")
	}
	return copyCheckpoint, nil
}

func validateJSONLLine(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode jsonl entry: %w", err)
	}
	if len(encoded) >= maxJSONLLineBytes {
		return errors.New("responses compaction entry is too large")
	}
	return nil
}

func (s *Session) AppendEvent(typ, reason string, ok bool) (Entry, error) {
	return s.AppendDetailsEvent(typ, map[string]any{"reason": reason, "ok": ok})
}

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
	if len(b)+1 > maxJSONLLineBytes {
		return Entry{}, fmt.Errorf("events.jsonl record exceeds %d bytes", maxJSONLLineBytes)
	}
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

func AppendCustomEntry(dir, extensionName, customType string, data any) (Entry, error) {
	details := map[string]any{"extension": extensionName, "customType": customType, "data": data}
	return AppendSidebandEvent(dir, "custom", details)
}

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

func (s *Session) AppendRequestHeader(system string, tools []ToolSchema, metadata ...RequestMeta) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rawTools, err := json.Marshal(tools)
	if err != nil {
		return Entry{}, err
	}
	tools, err = s.prompts.schemas(rawTools)
	if err != nil {
		return Entry{}, err
	}
	system = s.prompts.system(system)
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

func (s *Session) SetModel(provider, model string) error {
	return s.AppendModelChange(provider, model)
}

func (s *Session) SetModelAndThinking(provider, model, effort string) error {
	return s.AppendModelChange(provider, model, effort)
}

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

func (s *Session) SetTitle(title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Config.Title = strings.TrimSpace(title)
	return s.writeConfig()
}

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

func Remove(dir string) error {
	gate := fileGate(dir)
	gate.Lock()
	defer gate.Unlock()
	var err error
	for attempt := 0; attempt < 40; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}

func (s *Session) appendLocked(e Entry) error {
	return s.appendBatchLocked([]Entry{e})
}

func (s *Session) appendBatchLocked(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	var b []byte
	for _, entry := range entries {
		entry.CacheMiss = nil
		line, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if len(line) >= maxJSONLLineBytes {
			return errors.New("session entry is too large")
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	nextLeaf := entries[len(entries)-1].ID
	nextConfig, err := s.appendRawAndConfigAtLeaf(b, s.leafID, nextLeaf)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entry.CacheMiss = nil
		s.entries = append(s.entries, entry)
		s.byID[entry.ID] = entry
	}
	s.leafID = nextLeaf
	s.Config = nextConfig
	return nil
}

func (s *Session) appendPreparedLocked(e Entry, expectedLeaf string) error {
	e.CacheMiss = nil
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) >= maxJSONLLineBytes {
		return errors.New("compaction entry is too large")
	}
	nextConfig, err := s.appendRawAndConfigAtLeaf(append(b, '\n'), expectedLeaf, e.ID)
	if err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
	s.leafID = e.ID
	s.Config = nextConfig
	return nil
}

func (s *Session) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.appendRaw(append(b, '\n'))
}

func (s *Session) appendRaw(b []byte) error {
	gate := fileGate(s.Dir)
	gate.Lock()
	defer gate.Unlock()
	f, err := os.OpenFile(filepath.Join(s.Dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Session) appendRawAndConfigAtLeaf(b []byte, expectedLeaf, nextLeaf string) (Config, error) {
	gate := fileGate(s.Dir)
	gate.Lock()
	defer gate.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "config.json"))
	if err != nil {
		return Config{}, err
	}
	var current Config
	if err := json.Unmarshal(raw, &current); err != nil {
		return Config{}, fmt.Errorf("decode session config: %w", err)
	}
	activeLeaf := current.ActiveLeafID
	if activeLeaf == "" && expectedLeaf != "" {
		activeLeaf, err = lastActiveLeaf(s.Dir)
		if err != nil {
			return Config{}, err
		}
	}
	if activeLeaf != expectedLeaf {
		return Config{}, errSessionChanged
	}
	current.ActiveLeafID = nextLeaf
	configJSON, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := s.appendRawAndConfigLocked(b, configJSON); err != nil {
		return Config{}, err
	}
	return current, nil
}

func lastActiveLeaf(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
	first := true
	leaf := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return "", fmt.Errorf("decode events.jsonl entry: %w", err)
		}
		if !entry.Sideband {
			leaf = entry.ID
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return leaf, nil
}

func (s *Session) appendRawAndConfigLocked(b, configJSON []byte) error {
	f, err := os.OpenFile(filepath.Join(s.Dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		if truncateErr := f.Truncate(info.Size()); truncateErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback jsonl append: %w", truncateErr))
		}
		if syncErr := f.Sync(); syncErr != nil {
			return errors.Join(cause, fmt.Errorf("sync jsonl rollback: %w", syncErr))
		}
		return cause
	}
	n, err := f.Write(b)
	if err != nil {
		return rollback(err)
	}
	if n != len(b) {
		return rollback(io.ErrShortWrite)
	}
	if err := f.Sync(); err != nil {
		return rollback(err)
	}
	if err := writeFileAtomic(filepath.Join(s.Dir, "config.json"), append(configJSON, '\n')); err != nil {
		return rollback(err)
	}
	return nil
}

func (s *Session) writeConfig() error {
	b, err := json.MarshalIndent(s.Config, "", "  ")
	if err != nil {
		return err
	}
	gate := fileGate(s.Dir)
	gate.Lock()
	defer gate.Unlock()
	return writeFileAtomic(filepath.Join(s.Dir, "config.json"), append(b, '\n'))
}

func (s *Session) SaveConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeConfig()
}

func Fork(root string, src *Session) (*Session, error) {
	return ForkAt(root, src, src.LeafID())
}

func ForkAt(root string, src *Session, target string, requestedMode ...string) (*Session, error) {
	return forkAtContext(context.Background(), root, src, target, nil, nil, requestedMode...)
}

func ForkHistoryAt(root string, src *Session, target string, requestedMode ...string) (*Session, error) {
	return ForkHistoryAtWithMetadata(root, src, target, nil, requestedMode...)
}

func ForkHistoryAtContext(ctx context.Context, root string, src *Session, target string, metadata map[string]any, requestedMode ...string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return forkAtContext(ctx, root, src, target, func(e Entry) bool { return e.Type == "message" || e.Type == "compaction" }, metadata, requestedMode...)
}

func ForkHistoryAtWithMetadata(root string, src *Session, target string, metadata map[string]any, requestedMode ...string) (*Session, error) {
	return forkAtContext(context.Background(), root, src, target, func(e Entry) bool {
		return e.Type == "message" || e.Type == "compaction"
	}, metadata, requestedMode...)
}

func forkAtContext(ctx context.Context, root string, src *Session, target string, keep func(Entry) bool, metadata map[string]any, requestedMode ...string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	cacheKey := cfg.CacheKey
	if cacheKey == "" {
		cacheKey = parent
	}
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
	if keep != nil {
		filtered := make([]Entry, 0, len(entries))
		prev := ""
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				src.mu.Unlock()
				return nil, err
			}
			if !keep(e) {
				continue
			}
			e.ParentID = prev
			prev = e.ID
			filtered = append(filtered, e)
		}
		entries = filtered
	}
	sourceDir := src.Dir
	src.mu.Unlock()

	dst, err := CreateWithOptions(root, cwd, cfg.Provider, cfg.Model, CreateOptions{
		ThinkingEffort: cfg.ThinkingEffort, Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}
	dir := dst.Dir
	fail := func(err error) (*Session, error) {
		_ = dst.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	attachmentRefs := referencedAttachments(entries, sourceDir)
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		rewriteEntryAttachmentPaths(&entries[i], sourceDir, dir)
	}
	dst.Config = cfg
	if metadata != nil {
		dst.Config.Metadata = cloneMetadata(metadata)
	}
	dst.Config.ActiveLeafID = ""
	dst.Config.CacheKey = cacheKey
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
	dst.mu.Lock()
	err = dst.appendBatchLocked(entries)
	dst.mu.Unlock()
	if err != nil {
		return fail(err)
	}
	if len(attachmentRefs) > 0 {
		if err := copyAttachmentRefs(filepath.Join(sourceDir, "attachments"), filepath.Join(dir, "attachments"), attachmentRefs); err != nil {
			return fail(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return dst, nil
}

func CreateChild(root string, src *Session) (*Session, error) {
	return CreateChildWithMetadata(root, src, nil)
}

func CreateChildWithMetadata(root string, src *Session, metadata map[string]any) (*Session, error) {
	src.mu.Lock()
	cwd := src.Header.CWD
	parent := src.Header.ID
	cfg := src.Config
	src.mu.Unlock()

	dst, err := CreateWithOptions(root, cwd, cfg.Provider, cfg.Model, CreateOptions{
		ThinkingEffort: cfg.ThinkingEffort, Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}
	dir := dst.Dir
	fail := func(err error) (*Session, error) {
		_ = dst.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	dst.Config = cfg
	if metadata != nil {
		dst.Config.Metadata = cloneMetadata(metadata)
	}
	dst.Config.ActiveLeafID = ""
	dst.Config.CacheKey = ""
	dst.Header.ParentSession = parent
	dst.Header.ForkMode = ForkModeTree
	if err := dst.writeConfig(); err != nil {
		return fail(err)
	}
	if err := rewriteHeader(dst); err != nil {
		return fail(err)
	}
	if err := dst.Close(); err != nil {
		return fail(err)
	}
	dst, err = Open(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
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
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
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
	return out.Close()
}

func (t Toggle) Allowed(name string) bool {
	if len(t.Only) > 0 && !slices.Contains(t.Only, name) {
		return false
	}
	return !slices.Contains(t.Disabled, name)
}

func ForkRecentHistoryAtContext(ctx context.Context, root string, src *Session, target string, turns int, metadata map[string]any) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src.mu.Lock()
	entries := slices.Clone(src.leafEntriesLocked(target))
	src.mu.Unlock()
	start := 0
	remaining := turns
	for i := len(entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entries[i].Type == "message" && entries[i].Message != nil && entries[i].Message.Role == "user" && !entries[i].Message.ContextOnly {
			remaining--
			if remaining == 0 {
				start = i
				break
			}
		}
	}
	keep := map[string]bool{}
	for _, e := range entries[start:] {
		if e.Type == "message" || e.Type == "compaction" {
			keep[e.ID] = true
		}
	}
	return forkAtContext(ctx, root, src, target, func(e Entry) bool { return keep[e.ID] }, metadata, ForkModeTree)
}

func ForkRecentHistoryAt(root string, src *Session, target string, turns int) (*Session, error) {
	src.mu.Lock()
	entries := slices.Clone(src.leafEntriesLocked(target))
	src.mu.Unlock()
	start := 0
	remaining := turns
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "message" && entries[i].Message != nil && entries[i].Message.Role == "user" && !entries[i].Message.ContextOnly {
			remaining--
			if remaining == 0 {
				start = i
				break
			}
		}
	}
	keep := map[string]bool{}
	for _, e := range entries[start:] {
		if e.Type == "message" || e.Type == "compaction" {
			keep[e.ID] = true
		}
	}
	return forkAtContext(context.Background(), root, src, target, func(e Entry) bool { return keep[e.ID] }, nil, ForkModeTree)
}
