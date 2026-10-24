package output

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"ki/internal/types"
)

const (
	DefaultPreviewBytes          = 16 * 1024
	DefaultPreviewLines          = 800
	DefaultMaxFileBytes    int64 = 8 << 20
	DefaultMaxSessionBytes int64 = 256 << 20
	DefaultTTL                   = 24 * time.Hour
)

var ErrSessionBudget = errors.New("session tool output budget exhausted")

type Config struct {
	Root            string
	PreviewBytes    int
	PreviewLines    int
	MaxFileBytes    int64
	MaxSessionBytes int64
	TTL             time.Duration
	Now             func() time.Time
}

type Reference struct {
	Path         string `json:"path"`
	Bytes        int    `json:"bytes"`
	TotalBytes   int    `json:"totalBytes"`
	Lines        int    `json:"lines"`
	PreviewBytes int    `json:"previewBytes"`
	PreviewLines int    `json:"previewLines"`
	Truncated    bool   `json:"truncated"`
	Incomplete   bool   `json:"incomplete,omitempty"`
}

type Store struct {
	root            string
	ownerPath       string
	host            string
	previewBytes    int
	previewLines    int
	maxFileBytes    int64
	maxSessionBytes int64
	ttl             time.Duration
	nowFn           func() time.Time
	seq             atomic.Uint64

	mu           sync.Mutex
	sessionBytes map[string]int64
}

func New() (*Store, error) {
	return NewWithConfig(Config{})
}

func NewWithConfig(cfg Config) (*Store, error) {
	if cfg.Root == "" {
		cfg.Root = filepath.Join(os.TempDir(), baseDirName)
	}
	if cfg.PreviewBytes <= 0 {
		cfg.PreviewBytes = DefaultPreviewBytes
	}
	if cfg.PreviewLines <= 0 {
		cfg.PreviewLines = DefaultPreviewLines
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = DefaultMaxFileBytes
	}
	if cfg.MaxSessionBytes <= 0 {
		cfg.MaxSessionBytes = DefaultMaxSessionBytes
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	store := &Store{
		host:            host,
		previewBytes:    cfg.PreviewBytes,
		previewLines:    cfg.PreviewLines,
		maxFileBytes:    cfg.MaxFileBytes,
		maxSessionBytes: cfg.MaxSessionBytes,
		ttl:             cfg.TTL,
		nowFn:           cfg.Now,
		sessionBytes:    map[string]int64{},
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create tool output root: %w", err)
	}
	if err := os.Chmod(cfg.Root, 0o700); err != nil {
		return nil, fmt.Errorf("protect tool output root: %w", err)
	}
	store.sweep(cfg.Root)
	root, err := os.MkdirTemp(cfg.Root, fmt.Sprintf("%s%d-", runDirPrefix, os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("create tool output run root: %w", err)
	}
	if chmodErr := os.Chmod(root, 0o700); chmodErr != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("protect tool output run root: %w", chmodErr)
	}
	store.root = root
	store.ownerPath = filepath.Join(root, ownerFileName)
	if err := store.writeOwner(); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return store, nil
}

func (s *Store) now() time.Time {
	if s == nil || s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Store) Owns(path string) bool {
	if s == nil || s.root == "" || path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (s *Store) Close() error {
	if s == nil || s.root == "" {
		return nil
	}
	root := s.root
	s.root = ""
	s.mu.Lock()
	s.sessionBytes = map[string]int64{}
	s.mu.Unlock()
	return os.RemoveAll(root)
}

func (s *Store) CloseSession(sessionID string) error {
	if s == nil || sessionID == "" {
		return nil
	}
	s.mu.Lock()
	delete(s.sessionBytes, sessionID)
	s.mu.Unlock()
	if s.root == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.root, safePart(sessionID)))
}

func (s *Store) CreateOutputFile(sessionID, toolName string) (*os.File, error) {
	if s == nil {
		return nil, errors.New("tool output store is unavailable")
	}
	path, err := s.newPath(sessionID, toolName)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create tool output file: %w", err)
	}
	s.touchOwner()
	return file, nil
}

func (s *Store) Normalize(sessionID, toolName string, args map[string]any, content []types.Content, details any) ([]types.Content, *Reference) {
	if s == nil || s.skip(toolName, args, details) {
		return content, nil
	}
	raw, firstText, ok := joinText(content)
	if !ok {
		return content, nil
	}
	lines := lineCount(raw)
	if len(raw) <= s.previewBytes && lines <= s.previewLines {
		return content, nil
	}
	preview := previewHead(raw, s.previewBytes, s.previewLines)
	path, fileBytes, incomplete, err := s.spill(sessionID, toolName, raw)
	note := truncationNote(len(raw), lines, len(preview), path, fileBytes, incomplete, err)
	bounded := replaceText(content, firstText, preview+"\n\n"+note)
	if err != nil {
		return bounded, nil
	}
	return bounded, &Reference{
		Path: path, Bytes: fileBytes, TotalBytes: len(raw), Lines: lines,
		PreviewBytes: len(preview), PreviewLines: lineCount(preview), Truncated: true, Incomplete: incomplete,
	}
}

func (s *Store) spill(sessionID, toolName, raw string) (path string, bytes int, incomplete bool, err error) {
	keep := raw
	if int64(len(keep)) > s.maxFileBytes {
		keep = validUTF8Head(raw, int(s.maxFileBytes))
		incomplete = true
	}
	path, err = s.newPath(sessionID, toolName)
	if err != nil {
		return "", 0, incomplete, err
	}
	if err = s.reserve(sessionID, int64(len(keep))); err != nil {
		return "", 0, incomplete, err
	}
	if err = os.WriteFile(path, []byte(keep), 0o600); err != nil {
		_ = os.Remove(path)
		s.release(sessionID, int64(len(keep)))
		return "", 0, incomplete, fmt.Errorf("write tool output: %w", err)
	}
	s.touchOwner()
	return path, len(keep), incomplete, nil
}

func (s *Store) reserve(sessionID string, n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := s.sessionBytes[sessionID]
	if used+n > s.maxSessionBytes {
		return ErrSessionBudget
	}
	s.sessionBytes[sessionID] = used + n
	return nil
}

func (s *Store) release(sessionID string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := s.sessionBytes[sessionID] - n
	if used <= 0 {
		delete(s.sessionBytes, sessionID)
		return
	}
	s.sessionBytes[sessionID] = used
}

func (s *Store) skip(toolName string, args map[string]any, details any) bool {
	if details != nil {
		raw, err := json.Marshal(details)
		if err == nil {
			var value any
			if json.Unmarshal(raw, &value) == nil {
				if hasStringKey(value, "output_file") || hasNumberKey(value, "next_offset") {
					return true
				}
			}
		}
	}
	if strings.EqualFold(toolName, "Read") {
		if path, ok := args["file_path"].(string); ok && s.Owns(path) {
			return true
		}
	}
	return false
}

func (s *Store) newPath(sessionID, toolName string) (string, error) {
	if s.root == "" {
		return "", errors.New("tool output store is closed")
	}
	if sessionID == "" {
		sessionID = "unscoped"
	}
	dir := filepath.Join(s.root, safePart(sessionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create tool output session directory: %w", err)
	}
	seq := s.seq.Add(1)
	name := fmt.Sprintf("%06d-%s-%s.txt", seq, safePart(toolName), randomSuffix())
	return filepath.Join(dir, name), nil
}

func MergeDetails(details any, ref *Reference) any {
	if ref == nil {
		return details
	}
	merged := map[string]any{"spill": ref}
	if details == nil {
		return merged
	}
	raw, err := json.Marshal(details)
	if err != nil {
		merged["details"] = details
		return merged
	}
	var existing map[string]any
	if err := json.Unmarshal(raw, &existing); err != nil {
		merged["details"] = details
		return merged
	}
	for key, value := range existing {
		if key == "spill" {
			continue
		}
		merged[key] = value
	}
	return merged
}

func joinText(content []types.Content) (string, int, bool) {
	var text strings.Builder
	first := -1
	for i, block := range content {
		if block.Type == "" || block.Type == "text" {
			if first < 0 {
				first = i
			}
			text.WriteString(block.Text)
		}
	}
	if first < 0 {
		return "", 0, false
	}
	return text.String(), first, true
}

func replaceText(content []types.Content, firstText int, text string) []types.Content {
	bounded := make([]types.Content, 0, len(content))
	written := false
	for i, block := range content {
		switch {
		case block.Type == "" || block.Type == "text":
			if i == firstText && !written {
				bounded = append(bounded, types.Content{Type: "text", Text: text})
				written = true
			}
		default:
			bounded = append(bounded, block)
		}
	}
	return bounded
}

func truncationNote(total, lines, previewLen int, path string, fileBytes int, incomplete bool, err error) string {
	var reason string
	switch {
	case err != nil:
		reason = fmt.Sprintf(" full output not saved: %v.", err)
	case incomplete:
		reason = fmt.Sprintf(" file keeps the first %d bytes of %d (single-file limit).", fileBytes, total)
	}
	where := ""
	if path != "" {
		where = fmt.Sprintf(" Stored: %s. Use Read with file_path and offset to continue.", path)
	}
	return fmt.Sprintf("[tool output truncated: %d bytes, %d lines; showing %d bytes.%s%s]", total, lines, previewLen, reason, where)
}

func hasStringKey(value any, key string) bool {
	found := false
	walkDetails(value, func(k string, v any) {
		if k != key {
			return
		}
		if text, ok := v.(string); ok && text != "" {
			found = true
		}
	})
	return found
}

func hasNumberKey(value any, key string) bool {
	found := false
	walkDetails(value, func(k string, v any) {
		if k != key {
			return
		}
		if number, ok := v.(float64); ok && number != 0 {
			found = true
		}
	})
	return found
}

func walkDetails(value any, visit func(key string, value any)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			visit(key, item)
			walkDetails(item, visit)
		}
	case []any:
		for _, item := range typed {
			walkDetails(item, visit)
		}
	}
}

func safePart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

func randomSuffix() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "output"
	}
	return hex.EncodeToString(raw[:])
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func previewHead(text string, maxBytes, maxLines int) string {
	if maxBytes <= 0 || maxLines <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = validUTF8Head(out, maxBytes)
	}
	return out
}

func validUTF8Head(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
