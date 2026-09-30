package telemetry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"ki/internal/types"
)

const (
	FileName          = "telemetry.jsonl"
	maxTelemetryBytes = 32 << 20
)

// ToolDiagnostic classifies an execution without changing the model-facing
// ToolResult.IsError contract.
type ToolDiagnostic struct {
	Status      string
	Kind        string
	FaultDomain string
	Retryable   bool
}

// ModelRequest is the privacy-safe shape needed to diagnose provider cache use.
type ModelRequest struct {
	Provider, Model, API  string
	StaticHash, ShapeHash string
	BindingHash           string
	HistoryChunks         []string
	Usage                 *types.Usage
	DurationMS            int64
	Attempt               int
	Failed                bool
	ErrorKind             string
}

type cacheState struct {
	staticHash   string
	shapeHash    string
	bindingHash  string
	chunks       []string
	promptTokens int
	requestID    string
	generation   int64
	resetReason  string
}

var sessionCache sync.Map // telemetry path -> *cacheState

// Run writes correlated records for one occupied session run.
type Run struct {
	path, lockPath, sessionID, runID string
	traceID                          string
	cache                            *cacheState
	mu                               sync.Mutex
	requests, toolCalls              int64
	cacheRead, uncached              int64
	expectedResets, unexpectedMisses int64
	invalidArgs, preconditions       int64
	commandNonzero, harnessFailures  int64
	cancellations                    int64
	dropped                          atomic.Int64
	started                          time.Time
}

// NewRun creates a best-effort recorder. It does not open the file until the
// first record, so telemetry setup can never block session creation.
func NewRun(dir, sessionID, runID string) *Run {
	path := filepath.Join(dir, FileName)
	state, _ := sessionCache.LoadOrStore(path, &cacheState{})
	return &Run{
		path: path, lockPath: path + ".lock", sessionID: sessionID, runID: runID,
		traceID: randomID(16), cache: state.(*cacheState), started: time.Now(),
	}
}

// Forget releases process-local cache comparison state for a deleted session.
func Forget(dir string) {
	sessionCache.Delete(filepath.Join(dir, FileName))
}

// Hash returns a short deterministic digest suitable for equality diagnostics.
func Hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Reset marks the next model request as an expected cache boundary.
func (r *Run) Reset(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cache.generation++
	r.cache.resetReason = reason
	r.mu.Unlock()
}

// RecordModelRequest classifies cache reuse and writes one OTLP log record.
func (r *Run) RecordModelRequest(obs ModelRequest) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	requestID := randomID(8)
	common := commonPrefix(r.cache.chunks, obs.HistoryChunks)
	classification, reset := classifyCache(r.cache, obs, common)
	if reset {
		r.expectedResets++
	}
	if classification == "unexpected_full_miss" {
		r.unexpectedMisses++
	}
	promptTokens := 0
	if obs.Usage != nil {
		promptTokens = obs.Usage.Input + obs.Usage.CacheRead + obs.Usage.CacheWrite
		r.cacheRead += int64(obs.Usage.CacheRead)
		r.uncached += int64(obs.Usage.Input)
	}
	attrs := map[string]any{
		"event.name":                      "ki.harness.model_request",
		"gen_ai.provider.name":            obs.Provider,
		"gen_ai.operation.name":           "chat",
		"gen_ai.request.model":            obs.Model,
		"ki.gen_ai.api":                   obs.API,
		"ki.cache.classification":         classification,
		"ki.cache.prompt_generation":      r.cache.generation,
		"ki.cache.static_prefix_hash":     obs.StaticHash,
		"ki.cache.history_prefix_hash":    Hash(obs.HistoryChunks),
		"ki.cache.request_shape_hash":     obs.ShapeHash,
		"ki.cache.history_chunk_count":    len(obs.HistoryChunks),
		"ki.cache.common_prefix_chunks":   common,
		"ki.cache.previous_request_id":    r.cache.requestID,
		"ki.cache.previous_prompt_tokens": r.cache.promptTokens,
		"ki.cache.reset_reason":           r.cache.resetReason,
		"gen_ai.usage.input_tokens":       usageField(obs.Usage, "input"),
		"gen_ai.usage.output_tokens":      usageField(obs.Usage, "output"),
		"gen_ai.usage.cache_read_tokens":  usageField(obs.Usage, "cache_read"),
		"gen_ai.usage.cache_write_tokens": usageField(obs.Usage, "cache_write"),
		"ki.model_request.duration_ms":    obs.DurationMS,
		"ki.model_request.attempt":        obs.Attempt,
		"ki.model_request.failed":         obs.Failed,
		"ki.model_request.error_kind":     obs.ErrorKind,
		"ki.model_request.id":             requestID,
	}
	severity := "INFO"
	if classification == "unexpected_full_miss" {
		severity = "WARN"
	}
	if obs.Failed {
		severity = "WARN"
	}
	r.writeLocked(severity, "model request completed", attrs)
	r.cache.staticHash = obs.StaticHash
	r.cache.shapeHash = obs.ShapeHash
	r.cache.bindingHash = obs.BindingHash
	r.cache.chunks = append(r.cache.chunks[:0], obs.HistoryChunks...)
	r.cache.promptTokens = promptTokens
	r.cache.requestID = requestID
	r.cache.resetReason = ""
}

// RecordTool writes one classified tool completion.
func (r *Run) RecordTool(name, callID string, durationMS int64, isError bool, diag ToolDiagnostic, attrs map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCalls++
	if diag.Status == "" {
		if isError {
			diag = ToolDiagnostic{Status: "failed", Kind: "unclassified", FaultDomain: "unknown"}
		} else {
			diag = ToolDiagnostic{Status: "completed", Kind: "success", FaultDomain: "none"}
		}
	}
	switch diag.Kind {
	case "invalid_arguments":
		r.invalidArgs++
	case "precondition_failed":
		r.preconditions++
	case "command_nonzero":
		r.commandNonzero++
	}
	if diag.Status == "cancelled" {
		r.cancellations++
	}
	if diag.FaultDomain == "harness" {
		r.harnessFailures++
	}
	values := map[string]any{
		"event.name":           "ki.harness.tool_call",
		"gen_ai.tool.name":     name,
		"gen_ai.tool.call.id":  callID,
		"ki.tool.status":       diag.Status,
		"ki.tool.kind":         diag.Kind,
		"ki.tool.fault_domain": diag.FaultDomain,
		"ki.tool.retryable":    diag.Retryable,
		"ki.tool.is_error":     isError,
		"ki.tool.duration_ms":  durationMS,
	}
	for key, value := range attrs {
		values[key] = value
	}
	severity := "INFO"
	if diag.FaultDomain == "harness" {
		severity = "WARN"
	}
	r.writeLocked(severity, "tool call completed", values)
}

// Close emits the run summary. Telemetry remains best-effort.
func (r *Run) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeLocked("INFO", "harness run completed", map[string]any{
		"event.name":                        "ki.harness.run_summary",
		"ki.run.model_requests":             r.requests,
		"ki.run.tool_calls":                 r.toolCalls,
		"ki.run.cache_read_tokens":          r.cacheRead,
		"ki.run.uncached_input_tokens":      r.uncached,
		"ki.run.expected_cache_resets":      r.expectedResets,
		"ki.run.unexpected_cache_misses":    r.unexpectedMisses,
		"ki.run.invalid_tool_arguments":     r.invalidArgs,
		"ki.run.tool_precondition_failures": r.preconditions,
		"ki.run.command_nonzero":            r.commandNonzero,
		"ki.run.harness_failures":           r.harnessFailures,
		"ki.run.cancellations":              r.cancellations,
		"ki.run.dropped_records":            r.dropped.Load(),
		"ki.run.duration_ms":                time.Since(r.started).Milliseconds(),
	})
}

func classifyCache(prev *cacheState, obs ModelRequest, common int) (string, bool) {
	if prev.requestID == "" {
		return "first_request", false
	}
	if prev.resetReason != "" {
		return "expected_reset." + prev.resetReason, true
	}
	if prev.staticHash != obs.StaticHash {
		return "expected_reset.system_or_tools_change", true
	}
	if prev.bindingHash != obs.BindingHash {
		return "expected_reset.binding_change", true
	}
	if prev.shapeHash != obs.ShapeHash {
		return "expected_reset.request_shape_change", true
	}
	if common < len(prev.chunks) {
		return "expected_reset.history_change", true
	}
	if obs.Usage == nil {
		return "usage_unavailable", false
	}
	if obs.Usage.CacheRead == 0 && prev.promptTokens >= 4096 {
		return "unexpected_full_miss", false
	}
	if obs.Usage.CacheRead > 0 && obs.Usage.CacheRead < prev.promptTokens {
		return "partial_hit", false
	}
	return "hit", false
}

func commonPrefix(a, b []string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func usageField(u *types.Usage, field string) int {
	if u == nil {
		return 0
	}
	switch field {
	case "input":
		return u.Input
	case "output":
		return u.Output
	case "cache_read":
		return u.CacheRead
	default:
		return u.CacheWrite
	}
}

func (r *Run) writeLocked(severity, body string, attrs map[string]any) {
	attrs["ki.session.id"] = r.sessionID
	attrs["ki.run.id"] = r.runID
	record := otlpRequest{
		ResourceLogs: []resourceLogs{{
			Resource: resource{Attributes: attributes(map[string]any{
				"service.name": "ki", "ki.session.id": r.sessionID,
			})},
			ScopeLogs: []scopeLogs{{
				Scope: scope{Name: "ki.harness", Version: "1"},
				LogRecords: []logRecord{{
					TimeUnixNano:         strconv.FormatInt(time.Now().UnixNano(), 10),
					ObservedTimeUnixNano: strconv.FormatInt(time.Now().UnixNano(), 10),
					SeverityNumber:       severityNumber(severity),
					SeverityText:         severity,
					Body:                 stringValue(body),
					Attributes:           attributes(attrs),
					TraceID:              r.traceID,
					SpanID:               randomID(8),
				}},
			}},
		}},
	}
	data, err := json.Marshal(record)
	if err != nil {
		r.dropped.Add(1)
		return
	}
	data = append(data, '\n')
	lock := flock.New(r.lockPath)
	if err := lock.Lock(); err != nil {
		r.drop("lock", err)
		return
	}
	defer func() { _ = lock.Unlock() }()
	if info, err := os.Stat(r.path); err == nil && info.Size()+int64(len(data)) > maxTelemetryBytes {
		_ = os.Remove(r.path + ".1")
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			r.drop("rotate", err)
			return
		}
	}
	// The path is the indexed session directory, never request-controlled.
	//nolint:gosec // telemetry belongs beside the session transcript
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		r.drop("open", err)
		return
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		r.drop("write", writeErr)
	} else if closeErr != nil {
		r.drop("close", closeErr)
	}
}

func (r *Run) drop(operation string, err error) {
	r.dropped.Add(1)
	slog.Warn("session telemetry", "session_id", r.sessionID, "operation", operation, "err", err)
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = sum[:n]
	}
	// OTLP/JSON uses protobuf JSON encoding for bytes fields.
	return base64.StdEncoding.EncodeToString(b)
}

func severityNumber(severity string) int {
	switch severity {
	case "WARN":
		return 13
	case "ERROR":
		return 17
	case "DEBUG":
		return 5
	default:
		return 9
	}
}

type otlpRequest struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}
type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}
type resource struct {
	Attributes []keyValue `json:"attributes,omitempty"`
}
type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}
type scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type logRecord struct {
	TimeUnixNano         string     `json:"timeUnixNano"`
	ObservedTimeUnixNano string     `json:"observedTimeUnixNano"`
	SeverityNumber       int        `json:"severityNumber"`
	SeverityText         string     `json:"severityText"`
	Body                 anyValue   `json:"body"`
	Attributes           []keyValue `json:"attributes,omitempty"`
	TraceID              string     `json:"traceId"`
	SpanID               string     `json:"spanId"`
}
type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}
type anyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

func attributes(values map[string]any) []keyValue {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value != nil && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]keyValue, 0, len(keys))
	for _, key := range keys {
		if value, ok := otlpValue(values[key]); ok {
			out = append(out, keyValue{Key: key, Value: value})
		}
	}
	return out
}

func stringValue(value string) anyValue { return anyValue{StringValue: &value} }

func otlpValue(value any) (anyValue, bool) {
	switch v := value.(type) {
	case string:
		return anyValue{StringValue: &v}, true
	case bool:
		return anyValue{BoolValue: &v}, true
	case int:
		s := strconv.Itoa(v)
		return anyValue{IntValue: &s}, true
	case int64:
		s := strconv.FormatInt(v, 10)
		return anyValue{IntValue: &s}, true
	case float64:
		return anyValue{DoubleValue: &v}, true
	default:
		return anyValue{}, false
	}
}
