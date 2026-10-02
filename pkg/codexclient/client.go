package codexclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/klauspost/compress/zstd"
	"ki/internal/state"
)

const Originator = "codex_cli_rs"
const BetaFeatures = "remote_compaction_v2"
const WebSocketBeta = "responses_websockets=2026-02-06"

type identityDocument struct {
	Version        int    `json:"version"`
	InstallationID string `json:"installation_id"`
}

type sessionState struct {
	window string
	turns  map[string]*turnState
	used   time.Time
}

type turnState struct {
	sticky string
	used   time.Time
}

var sessions = struct {
	sync.Mutex
	values map[string]*sessionState
}{values: make(map[string]*sessionState)}

// UUID returns a fresh request identity without relying on host-specific paths.
func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func installationID() (string, error) {
	home := os.Getenv("KI_HOME")
	if home == "" {
		base, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(base, ".ki")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "codex-client.json")
	read := func() (string, error) {
		raw, _, err := state.ReadFile(path, 1, nil)
		if err != nil {
			return "", err
		}
		var d identityDocument
		if err := json.Unmarshal(raw, &d); err != nil {
			return "", err
		}
		if !validUUID(d.InstallationID) {
			return "", fmt.Errorf("invalid Codex installation identity in %s", path)
		}
		return d.InstallationID, nil
	}
	id, err := read()
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	// Multiple extension sidecars can start together. Lock and re-read so all
	// processes use one installation identity instead of replacing each other.
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return "", err
	}
	if !locked {
		return "", errors.New("timed out locking Codex installation identity")
	}
	defer lock.Unlock()
	id, err = read()
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	id = UUID()
	if err := state.WriteVersioned(path, 1, identityDocument{1, id}, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func validUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16
}

// UserAgent follows the Codex CLI shape. The source checkout's version is
// 0.0.0; no release, attestation, or terminal capability is invented.
func UserAgent() string {
	system, version := hostOS()
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	terminal := os.Getenv("TERM_PROGRAM")
	if terminal == "" {
		terminal = os.Getenv("TERM")
	}
	if terminal == "" {
		terminal = "unknown"
	}
	ua := fmt.Sprintf("%s/0.0.0 (%s %s; %s) %s", Originator, system, version, arch, terminal)
	return strings.Map(func(r rune) rune {
		if r < ' ' || r > '~' {
			return '_'
		}
		return r
	}, ua)
}

// Prepare replaces host-owned metadata with a consistent request snapshot and
// returns auth-independent HTTP headers. Callers must split thinking presets
// before calling: service_tier is an upstream routing value, never a UI label.
func Prepare(body map[string]any, sessionID, turnID string, compaction bool) (map[string]string, error) {
	return PrepareScoped(body, sessionID, turnID, compaction, "")
}

// PrepareScoped isolates routing state by an opaque endpoint/credential/model
// binding. Concurrent logical turns cannot reset each other's sticky tokens.
func PrepareScoped(body map[string]any, sessionID, turnID string, compaction bool, scope string) (map[string]string, error) {
	id, err := installationID()
	if err != nil {
		return nil, fmt.Errorf("Codex client identity: %w", err)
	}
	ephemeral := sessionID == ""
	if ephemeral {
		sessionID = UUID()
	}
	if turnID == "" {
		turnID = UUID()
	}
	sessions.Lock()
	// Read-only direct searches have no session and are never retained. Bound
	// the least-recently-used metadata without rotating idle active windows.
	now := time.Now()
	key := sessionID + "\x00" + scope
	s := sessions.values[key]
	if s == nil {
		s = &sessionState{window: UUID(), turns: make(map[string]*turnState)}
		if !ephemeral {
			if len(sessions.values) >= 256 {
				var oldest string
				var stamp time.Time
				for key, candidate := range sessions.values {
					if oldest == "" || candidate.used.Before(stamp) {
						oldest, stamp = key, candidate.used
					}
				}
				delete(sessions.values, oldest)
			}
			sessions.values[key] = s
		}
	}
	s.used = now
	t := s.turns[turnID]
	if t == nil {
		if len(s.turns) >= 8 {
			var oldest string
			var stamp time.Time
			for key, candidate := range s.turns {
				if oldest == "" || candidate.used.Before(stamp) {
					oldest, stamp = key, candidate.used
				}
			}
			delete(s.turns, oldest)
		}
		t = &turnState{}
		s.turns[turnID] = t
	}
	t.used = now
	window, sticky := s.window, t.sticky
	sessions.Unlock()
	kind := "turn"
	if compaction {
		kind = "compaction"
	}
	metadata := map[string]any{
		"installation_id": id, "session_id": sessionID, "thread_id": sessionID,
		"window_id": window, "turn_id": turnID, "root_turn_id": turnID, "request_kind": kind,
	}
	if compaction {
		// The host does not currently expose trigger/reason. Omit those rather
		// than describing automatic checkpoints as manual user requests.
		metadata["compaction"] = map[string]any{
			"implementation": "responses_compaction_v2", "phase": "standalone_turn", "strategy": "memento",
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	body["client_metadata"] = map[string]any{
		"x-codex-installation-id": id, "session_id": sessionID, "thread_id": sessionID,
		"x-codex-window-id": window, "turn_id": turnID, "x-codex-turn-metadata": string(raw),
	}
	model, _ := body["model"].(string)
	tier, _ := body["service_tier"].(string)
	routing := "model=" + model
	if tier != "" && tier != "default" {
		routing += ";tier=" + tier
	} else {
		delete(body, "service_tier")
	}
	headers := map[string]string{
		"originator": Originator, "User-Agent": UserAgent(), "x-codex-beta-features": BetaFeatures,
		"session-id": sessionID, "thread-id": sessionID, "x-client-request-id": sessionID,
		"x-codex-window-id": window, "x-codex-turn-metadata": string(raw), "x-codex-routing-hint": routing,
	}
	if sticky != "" {
		headers["x-codex-turn-state"] = sticky
	}
	return headers, nil
}

// CaptureTurnState keeps the first server routing token for the logical turn.
// A delayed response from an old/canceled turn must not contaminate its successor.
func CaptureTurnState(sessionID, turnID, value string) {
	CaptureTurnStateScoped(sessionID, turnID, value, "")
}

// CaptureTurnStateScoped captures only within the request's actual binding.
func CaptureTurnStateScoped(sessionID, turnID, value, scope string) {
	if value == "" {
		return
	}
	sessions.Lock()
	defer sessions.Unlock()
	if s := sessions.values[sessionID+"\x00"+scope]; s != nil {
		if t := s.turns[turnID]; t != nil && t.sticky == "" {
			t.sticky = value
		}
	}
}

// AdvanceWindow starts a new affinity window after a checkpoint succeeds.
func AdvanceWindow(sessionID string) {
	sessions.Lock()
	defer sessions.Unlock()
	for key, s := range sessions.values {
		if strings.HasPrefix(key, sessionID+"\x00") {
			s.window = UUID()
			clear(s.turns)
		}
	}
}

// Encode places routing fields first, matching Codex's Responses serializers.
// Gateways can route without buffering an entire multi-megabyte input history.
func Encode(body map[string]any) ([]byte, error) {
	rest := make(map[string]any, len(body))
	for key, value := range body {
		rest[key] = value
	}
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	for _, key := range []string{"type", "model", "stream", "service_tier"} {
		value, ok := rest[key]
		if !ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&out, "%q:", key)
		out.Write(raw)
		delete(rest, key)
	}
	raw, err := json.Marshal(rest)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2 {
		if !first {
			out.WriteByte(',')
		}
		out.Write(raw[1 : len(raw)-1])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

var compressor struct {
	sync.Once
	encoder *zstd.Encoder
	err     error
}

// Compress uses the Codex HTTP fallback's zstd request encoding. The encoder
// uses one worker so tiny requests do not create a CPU-sized goroutine pool.
func Compress(raw []byte) ([]byte, error) {
	compressor.Do(func() {
		compressor.encoder, compressor.err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	})
	if compressor.err != nil {
		return nil, compressor.err
	}
	return compressor.encoder.EncodeAll(raw, nil), nil
}
