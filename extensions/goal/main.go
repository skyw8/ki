package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/google/uuid"
	"ki/internal/state"
	"ki/pkg/extensionrpc"
)

//go:embed prompts.json tools.json
var assets embed.FS
var templates map[string]string
var toolSpecs []any

func init() {
	b, _ := assets.ReadFile("prompts.json")
	if err := json.Unmarshal(b, &templates); err != nil {
		panic(err)
	}
	b, _ = assets.ReadFile("tools.json")
	if err := json.Unmarshal(b, &toolSpecs); err != nil {
		panic(err)
	}
}

const maxAutomaticTurns = 25
const maxWaitDelay = 2147483647
const minWaitDelay = 10000

type goalWait struct {
	Reason   string `json:"reason"`
	ResumeAt *int64 `json:"resumeAt,omitempty"`
}
type goalState struct {
	ID             string    `json:"id"`
	Text           string    `json:"text"`
	Status         string    `json:"status"`
	StartedAt      int64     `json:"startedAt"`
	UpdatedAt      int64     `json:"updatedAt"`
	Iteration      int       `json:"iteration"`
	AutomaticTurns int       `json:"automaticTurns"`
	Waiting        *goalWait `json:"waiting,omitempty"`
}
type store struct {
	Version int        `json:"version"`
	Goal    *goalState `json:"goal"`
}
type commandResult struct {
	Handled bool   `json:"handled"`
	Notice  string `json:"notice,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}
type hostCall func(context.Context, string, map[string]any, any) error
type app struct {
	mu           sync.Mutex
	goal         *goalState
	home, id     string
	call         hostCall
	timer        *time.Timer
	generation   uint64
	closed       bool
	asyncMu      sync.Mutex
	asyncQueue   []asyncGoalJob
	asyncRunning bool
	asyncCtx     context.Context
	asyncStop    context.CancelFunc
}
type asyncGoalJob struct {
	method string
	params map[string]any
	ctx    context.Context
	done   chan goalReply
}
type goalReply struct {
	value any
	err   error
}

// JavaScript trims Unicode space separators and BOM, but does not trim NEL.
// Preserve the original objective and tool-validation boundaries after the port.
func jsWhitespace(r rune) bool {
	return r >= '\t' && r <= '\r' || r == ' ' || r == '\uFEFF' || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Zs, r)
}
func jsTrim(s string) string   { return strings.TrimFunc(s, jsWhitespace) }
func text(v any) string        { s, _ := v.(string); return jsTrim(s) }
func stringValue(v any) string { s, _ := v.(string); return s }
func record(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }
func xml(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
func prompt(kind string, g *goalState, marker, reason string) string {
	return strings.NewReplacer("__ID__", xml(g.ID), "__OBJECTIVE__", xml(g.Text), "987654321", fmt.Sprint(g.Iteration), "__MARKER__", marker, "__REASON__", xml(reason)).Replace(templates[kind])
}
func appendSystem(system, addon string) string {
	base := strings.TrimRightFunc(system, jsWhitespace)
	if base == "" {
		return addon
	}
	return base + "\n\n" + addon
}
func parseCommand(args string) (kind, objective, errText string) {
	tokens := strings.FieldsFunc(args, jsWhitespace)
	if len(tokens) == 0 {
		return "show", "", ""
	}
	verb := tokens[0]
	rest := tokens[1:]
	switch verb {
	case "pause", "resume", "clear", "stop", "status":
		canonical := verb
		if verb == "stop" {
			canonical = "clear"
		}
		if len(rest) > 0 {
			return "", "", "Usage: /goal " + canonical
		}
		if canonical == "status" {
			canonical = "show"
		}
		return canonical, "", ""
	case "edit":
		kind = "edit"
		tokens = rest
	default:
		kind = "start"
	}
	objective = strings.Join(tokens, " ")
	if objective == "" {
		return "", "", "Usage: /goal edit <objective>"
	}
	if jsLength(objective) > 4000 {
		return "", "", "Objective is too long (max 4000 characters)."
	}
	return kind, objective, ""
}
func (a *app) host(ctx context.Context, method string, p map[string]any, out any) error {
	p["sessionId"] = a.id
	return a.call(ctx, method, p, out)
}
func (a *app) statePath() string { return filepath.Join(a.home, "goal", a.id+".json") }
func normalizeWaiting(raw json.RawMessage) *goalWait {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	var reason string
	if json.Unmarshal(fields["reason"], &reason) != nil {
		return nil
	}
	reason = jsTrim(reason)
	if reason == "" || jsLength(reason) > 1000 {
		return nil
	}
	wait := &goalWait{Reason: reason}
	if resume, exists := fields["resumeAt"]; exists {
		var value any
		if json.Unmarshal(resume, &value) != nil {
			return nil
		}
		n, ok := value.(float64)
		if !ok || math.Trunc(n) != n || n < 0 || n > 8640000000000000 {
			return nil
		}
		at := int64(n)
		wait.ResumeAt = &at
	}
	return wait
}
func (a *app) restore(ctx context.Context) (bool, error) {
	// A restored deadline may already be due. Hold the same lock as the timer
	// callback so it cannot change goal state while initialization reads it.
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, _, err := state.ReadFile(a.statePath(), 1, nil)
	if err == nil {
		var saved struct {
			Goal map[string]json.RawMessage `json:"goal"`
		}
		if json.Unmarshal(raw, &saved) == nil && saved.Goal != nil {
			var id string
			if idRaw, exists := saved.Goal["id"]; exists && string(idRaw) != "null" && json.Unmarshal(idRaw, &id) == nil {
				waiting := normalizeWaiting(saved.Goal["waiting"])
				delete(saved.Goal, "waiting")
				// Invalid waiting metadata must not discard an otherwise valid goal.
				// The original loader normalizes the optional wait independently.
				b, _ := json.Marshal(saved.Goal)
				var goal goalState
				if json.Unmarshal(b, &goal) == nil {
					goal.Waiting = waiting
					a.goal = &goal
				}
			}
		}
	}
	a.syncUI(ctx)
	a.restoreTimer()
	return a.goal != nil && a.goal.Status == "active" && a.goal.Waiting == nil, nil
}
func (a *app) clearTimer() {
	a.generation++
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}
func (a *app) close() {
	a.asyncMu.Lock()
	if a.asyncCtx == nil {
		a.asyncCtx, a.asyncStop = context.WithCancel(context.Background())
	}
	a.asyncStop()
	for _, job := range a.asyncQueue {
		if job.done != nil {
			job.done <- goalReply{err: context.Canceled}
		}
	}
	a.asyncQueue = nil
	a.asyncMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.clearTimer()
}
func (a *app) enqueueAsync(method string, params map[string]any) {
	a.queueJob(asyncGoalJob{method: method, params: params})
}
func (a *app) enqueueSync(ctx context.Context, method string, params map[string]any) (any, error) {
	done := make(chan goalReply, 1)
	a.queueJob(asyncGoalJob{method: method, params: params, ctx: ctx, done: done})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case reply := <-done:
		return reply.value, reply.err
	}
}
func (a *app) queueJob(job asyncGoalJob) {
	a.asyncMu.Lock()
	if a.asyncCtx == nil {
		a.asyncCtx, a.asyncStop = context.WithCancel(context.Background())
	}
	if a.asyncCtx.Err() != nil {
		a.asyncMu.Unlock()
		if job.done != nil {
			job.done <- goalReply{err: context.Canceled}
		}
		return
	}
	a.asyncQueue = append(a.asyncQueue, job)
	if a.asyncRunning {
		a.asyncMu.Unlock()
		return
	}
	a.asyncRunning = true
	a.asyncMu.Unlock()
	// UI confirmation may wait for human input. Keep that wait in this session's
	// ordered queue so it cannot stall lifecycle notifications for other sessions.
	go func() {
		for {
			a.asyncMu.Lock()
			if len(a.asyncQueue) == 0 || a.asyncCtx.Err() != nil {
				a.asyncRunning = false
				a.asyncMu.Unlock()
				return
			}
			job := a.asyncQueue[0]
			a.asyncQueue[0] = asyncGoalJob{}
			a.asyncQueue = a.asyncQueue[1:]
			ctx := a.asyncCtx
			a.asyncMu.Unlock()
			cancel := func() {}
			stop := func() bool { return false }
			if job.ctx != nil {
				ctx, cancel = context.WithCancel(job.ctx)
				stop = context.AfterFunc(a.asyncCtx, cancel)
			}
			value, err := a.handle(ctx, job.method, job.params)
			stop()
			cancel()
			if job.done != nil {
				job.done <- goalReply{value, err}
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "goal notification %s: %v\n", job.method, err)
			}
		}
	}()
}
func (a *app) restoreTimer() {
	a.clearTimer()
	if a.closed || a.goal == nil || a.goal.Status != "active" || a.goal.Waiting == nil || a.goal.Waiting.ResumeAt == nil {
		return
	}
	delay := *a.goal.Waiting.ResumeAt - time.Now().UnixMilli()
	if delay < 0 {
		delay = 0
	}
	if delay > maxWaitDelay {
		delay = maxWaitDelay
	}
	generation := a.generation
	a.timer = time.AfterFunc(time.Duration(delay)*time.Millisecond, func() {
		a.enqueueAsync("_goal.waitDue", map[string]any{"generation": generation})
	})
}
func (a *app) persist(ctx context.Context) error {
	if err := state.WriteVersioned(a.statePath(), 1, store{1, a.goal}, 0600); err != nil {
		return err
	}
	if err := a.host(ctx, "session.appendEntry", map[string]any{"customType": "goal-state", "data": map[string]any{"goal": a.goal}}, nil); err != nil {
		fmt.Fprintln(os.Stderr, "goal appendEntry:", err)
	}
	a.syncUI(ctx)
	return nil
}
func ui(key string) map[string]any { return map[string]any{"key": key} }
func formattedTime(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("1/2/2006, 3:04:05 PM")
}
func (a *app) syncUI(ctx context.Context) {
	g := a.goal
	status := ui("title")
	tone := "info"
	panel := map[string]any{"title": ui("title")}
	value := ""
	submit := "start"
	canPause, canResume := false, false
	if g == nil {
		panel["summary"] = ui("noGoal")
	} else {
		value = g.Text
		label := g.Status
		waiting := g.Waiting != nil
		if waiting {
			label = "waiting"
		}
		status = ui("statusLine." + label)
		tone = "active"
		if g.Status == "complete" {
			tone = "success"
		} else if waiting || g.Status != "active" {
			tone = "warning"
		}
		canPause = g.Status == "active" && !waiting
		canResume = g.Status == "paused" || g.Status == "blocked" || waiting
		if g.Status != "complete" {
			submit = "update"
		}
		items := []any{map[string]any{"label": ui("status"), "value": ui("status." + label)}}
		if waiting {
			items = append(items, map[string]any{"label": ui("waiting"), "value": g.Waiting.Reason})
			if g.Waiting.ResumeAt != nil && *g.Waiting.ResumeAt != 0 {
				items = append(items, map[string]any{"label": ui("resumeAt"), "value": formattedTime(*g.Waiting.ResumeAt)})
			}
		}
		for _, it := range []struct {
			k string
			v any
		}{{"turns", fmt.Sprintf("%d / %d", g.AutomaticTurns, maxAutomaticTurns)}, {"started", formattedTime(g.StartedAt)}, {"updated", formattedTime(g.UpdatedAt)}, {"id", g.ID}} {
			items = append(items, map[string]any{"label": ui(it.k), "value": it.v})
		}
		panel["sections"] = []any{map[string]any{"heading": ui("details"), "items": items}}
	}
	panel["fields"] = []any{map[string]any{"id": "objective", "label": ui("objective"), "type": "textarea", "value": value}}
	panel["submitLabel"] = ui(submit)
	pauseTitle, resumeTitle, clearTitle := "onlyActivePause", "nothingToResume", "clearHint"
	if g == nil {
		pauseTitle, resumeTitle, clearTitle = "noActive", "noPaused", "noGoalToClear"
	}
	if canPause {
		pauseTitle = "pauseHint"
	}
	if canResume {
		resumeTitle = "resumeHint"
	}
	panel["actions"] = []any{map[string]any{"id": "pause", "label": ui("pause"), "disabled": !canPause, "title": ui(pauseTitle)}, map[string]any{"id": "resume", "label": ui("resume"), "disabled": !canResume, "title": ui(resumeTitle)}, map[string]any{"id": "clear", "label": ui("clear"), "style": "danger", "disabled": g == nil, "title": ui(clearTitle)}}
	if err := a.host(ctx, "ui.setStatus", map[string]any{"key": "goal", "text": status, "tone": tone}, nil); err != nil {
		fmt.Fprintln(os.Stderr, "goal ui:", err)
		return
	}
	if err := a.host(ctx, "ui.setPanel", panel, nil); err != nil {
		fmt.Fprintln(os.Stderr, "goal ui:", err)
	}
}
func (a *app) command(ctx context.Context, args string) (commandResult, error) {
	kind, objective, e := parseCommand(args)
	if e != "" {
		return commandResult{Handled: true, Notice: e}, nil
	}
	return a.applyCommand(ctx, kind, objective)
}
func (a *app) applyCommand(ctx context.Context, kind, objective string) (commandResult, error) {
	out := commandResult{Handled: true}
	g := a.goal
	switch kind {
	case "show":
		if g == nil {
			out.Notice = "No /goal in this session. /goal <objective> to start."
		} else {
			wait := ""
			if g.Waiting != nil {
				wait = "\nwaiting: " + g.Waiting.Reason
			}
			out.Notice = "Goal · " + g.Status + wait + "\n" + g.Text
		}
		return out, nil
	case "start":
		if g != nil && g.Status != "complete" {
			out.Notice = "Goal already " + g.Status + ". /goal clear or /goal edit <objective>."
			return out, nil
		}
		a.clearTimer()
		now := time.Now().UnixMilli()
		a.goal = &goalState{ID: uuid.NewString(), Text: objective, Status: "active", StartedAt: now, UpdatedAt: now}
		out.Handled = false
		out.Prompt = prompt("start", a.goal, "", "")
	case "edit":
		if g == nil || g.Status == "complete" {
			out.Notice = "No active goal to edit. /goal <objective> to start one."
			return out, nil
		}
		a.clearTimer()
		g.ID = uuid.NewString()
		g.Text = objective
		g.Status = "active"
		g.Iteration = 0
		g.AutomaticTurns = 0
		g.Waiting = nil
		g.UpdatedAt = time.Now().UnixMilli()
		out.Handled = false
		out.Prompt = prompt("edit", g, "", "")
	case "pause":
		if g == nil || g.Status != "active" {
			out.Notice = "No active goal to pause."
			return out, nil
		}
		a.clearTimer()
		g.Status = "paused"
		g.Waiting = nil
		g.UpdatedAt = time.Now().UnixMilli()
		out.Notice = "Paused."
	case "resume":
		if g == nil {
			out.Notice = "No paused or blocked goal to resume."
			return out, nil
		}
		reason := ""
		if g.Waiting != nil {
			reason = g.Waiting.Reason
		}
		if g.Status != "paused" && g.Status != "blocked" && reason == "" {
			out.Notice = "No paused, blocked, or waiting goal to resume."
			return out, nil
		}
		a.clearTimer()
		from := g.Status
		g.Status = "active"
		g.AutomaticTurns = 0
		g.Waiting = nil
		g.UpdatedAt = time.Now().UnixMilli()
		out.Handled = false
		out.Prompt = prompt("resume-"+from, g, "", "")
		if reason != "" {
			out.Prompt = prompt("waiting", g, "", reason)
		}
	case "clear":
		if g == nil {
			out.Notice = "No goal to clear."
			return out, nil
		}
		a.clearTimer()
		a.goal = nil
		out.Notice = "Goal cleared."
	}
	return out, a.persist(ctx)
}
func textResult(s string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": isError}
}

const jsWhitespacePattern = `[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`

var contradictory = regexp.MustCompile(strings.ReplaceAll(`(?i)\bnot\s+(?:yet\s+)?(?:complete|completed|done|finished)\b|\bstill\s+(?:incomplete|failing|failing\s+tests?|fails?)\b|\btests?\s+(?:still\s+)?fail(?:ing)?\b`, `\s`, jsWhitespacePattern))
var couldPrefix = regexp.MustCompile(`(?i)could` + jsWhitespacePattern + `$`)

func contradictorySummary(s string) bool {
	for _, ix := range contradictory.FindAllStringIndex(s, -1) {
		prefix := s[:ix[0]]
		if strings.HasPrefix(strings.ToLower(s[ix[0]:ix[1]]), "not") && couldPrefix.MatchString(prefix) {
			continue
		}
		return true
	}
	return false
}
func (a *app) staleReason(id string) string {
	if a.goal == nil {
		return "no active goal"
	}
	if a.goal.Status != "active" {
		return "goal is " + a.goal.Status + ", not active"
	}
	if id == "" {
		return "missing goal_id"
	}
	if jsLength(id) > 128 {
		return "goal_id is too long"
	}
	if id != a.goal.ID {
		return "goal_id does not match the active goal"
	}
	return ""
}
func number(v any) float64 {
	n, ok := v.(float64)
	if !ok {
		return math.NaN()
	}
	return n
}
func (a *app) execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if name != "goal_complete" && name != "goal_blocked" && name != "goal_wait" {
		return textResult("unknown tool "+name, true), nil
	}
	prefix := name + " rejected: "
	if name == "goal_complete" {
		prefix = "Goal completion rejected: "
	}
	reject := func(why string) (any, error) { return textResult(prefix+why+".", true), nil }
	if stale := a.staleReason(text(args["goal_id"])); stale != "" {
		return reject(stale)
	}
	reason := text(args["reason"])
	result := ""
	switch name {
	case "goal_complete":
		summary := text(args["summary"])
		if summary == "" {
			return reject("summary is required")
		}
		if jsLength(summary) > 4000 {
			return reject("summary is too long")
		}
		if contradictorySummary(summary) {
			return reject("summary says the goal is not complete")
		}
		a.clearTimer()
		a.goal.Status = "complete"
		a.goal.Waiting = nil
		result = "Goal complete: " + summary
	case "goal_blocked":
		evidence := text(args["evidence"])
		n := number(args["repeated_turns"])
		if reason == "" {
			return reject("reason is empty")
		}
		if jsLength(reason) > 1000 {
			return reject("reason is too long")
		}
		if evidence == "" {
			return reject("evidence is empty")
		}
		if jsLength(evidence) > 4000 {
			return reject("evidence is too long")
		}
		if math.IsNaN(n) || math.Trunc(n) != n {
			return reject("repeated_turns must be a whole number")
		}
		if n < 3 {
			return reject("repeated_turns must be at least 3")
		}
		a.clearTimer()
		a.goal.Status = "blocked"
		a.goal.Waiting = nil
		result = "Goal blocked: " + reason
	case "goal_wait":
		if a.goal.Waiting != nil {
			return reject("goal is already waiting")
		}
		if reason == "" {
			return reject("reason is empty")
		}
		if jsLength(reason) > 1000 {
			return reject("reason is too long")
		}
		w := &goalWait{Reason: reason}
		result = "Goal waiting: " + reason
		if raw, ok := args["resume_after_ms"]; ok {
			n := number(raw)
			if math.IsNaN(n) || math.Trunc(n) != n || n < 1 || n > maxWaitDelay {
				return reject(fmt.Sprintf("resume_after_ms must be a whole number from 1 to %d", maxWaitDelay))
			}
			effective := int64(n)
			if effective < minWaitDelay {
				effective = minWaitDelay
				result += fmt.Sprintf("\nRequested resume_after_ms %.0f was clamped to %d.", n, effective)
			}
			at := time.Now().UnixMilli() + effective
			w.ResumeAt = &at
		}
		a.goal.Waiting = w
	}
	a.goal.UpdatedAt = time.Now().UnixMilli()
	if err := a.persist(ctx); err != nil {
		return nil, err
	}
	if name == "goal_wait" {
		a.restoreTimer()
	}
	return textResult(result, false), nil
}
func (a *app) enqueue(ctx context.Context, s, key, when string) error {
	return a.host(ctx, "session.enqueue", map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "deliverAs": "queue", "when": when, "idempotencyKey": key, "kind": "user"}, nil)
}
func (a *app) continueGoal(ctx context.Context) error {
	g := a.goal
	if g == nil || g.Status != "active" || g.Waiting != nil {
		return nil
	}
	if g.AutomaticTurns >= maxAutomaticTurns {
		g.Status = "paused"
		g.UpdatedAt = time.Now().UnixMilli()
		return a.persist(ctx)
	}
	g.AutomaticTurns++
	g.Iteration++
	g.UpdatedAt = time.Now().UnixMilli()
	if err := a.persist(ctx); err != nil {
		return err
	}
	marker := fmt.Sprintf("%s:%d", g.ID, g.Iteration)
	return a.enqueue(ctx, prompt("continue", g, marker, ""), "goal-continue:"+marker, "settled")
}

type sidecar struct {
	mu   sync.Mutex
	apps map[string]*app
	home string
	call hostCall
}

func (s *sidecar) prepare(ctx context.Context, id string) *app {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[id]; a != nil {
		return a
	}
	a := &app{home: s.home, id: id, call: s.call}
	s.apps[id] = a
	active, _ := a.restore(ctx)
	if active {
		time.AfterFunc(100*time.Millisecond, func() { a.enqueueAsync("_goal.restoreContinue", nil) })
	}
	return a
}
func (s *sidecar) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	p := map[string]any{}
	_ = json.Unmarshal(raw, &p)
	id := stringValue(p["sessionId"])
	// Timer jobs stay internal; unknown host RPC methods retain their no-op behavior.
	if strings.HasPrefix(method, "_goal.") {
		return map[string]any{}, nil
	}
	if method == "initialize" {
		if id == "" {
			id = os.Getenv("KI_SESSION_ID")
		}
		if id != "" {
			s.prepare(ctx, id)
		} else {
			_ = s.call(ctx, "ui.setGlobalStatus", map[string]any{"key": "goal", "text": ui("title"), "tone": "info"}, nil)
			_ = s.call(ctx, "ui.setGlobalPanel", map[string]any{"title": ui("title"), "summary": ui("noSession")}, nil)
		}
		return map[string]any{"tools": toolSpecs, "commands": []any{map[string]any{"name": "goal", "description": "Run a goal to completion", "argumentHint": "<objective>", "completions": []string{"pause", "resume", "clear", "edit", "status"}}}, "subscriptions": []any{map[string]any{"event": "before_agent_start", "mode": "sync"}, map[string]any{"event": "agent_settled", "mode": "async"}}}, nil
	}
	if method == "session.open" {
		s.prepare(ctx, id)
		return map[string]any{}, nil
	}
	if method == "shutdown" {
		s.close()
		return map[string]any{}, nil
	}
	s.mu.Lock()
	a := s.apps[id]
	if method == "session.close" {
		delete(s.apps, id)
	}
	s.mu.Unlock()
	if method == "session.close" {
		if a != nil {
			a.close()
		}
		return map[string]any{}, nil
	}
	if a == nil {
		if method == "command.invoke" {
			return commandResult{Handled: false}, nil
		}
		if method == "tool.execute" {
			return textResult("goal sidecar not ready", true), nil
		}
		return map[string]any{}, nil
	}
	if method == "ui.action" || method == "ui.submit" || method == "lifecycle.event" {
		a.enqueueAsync(method, p)
		return map[string]any{}, nil
	}
	if method == "command.invoke" || method == "tool.execute" {
		return a.enqueueSync(ctx, method, p)
	}
	return a.handle(ctx, method, p)
}
func (a *app) handle(ctx context.Context, method string, p map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || ctx.Err() != nil {
		return map[string]any{}, nil
	}
	switch method {
	case "_goal.waitDue":
		if generation, ok := p["generation"].(uint64); !ok || generation != a.generation {
			return map[string]any{}, nil
		}
		a.timer = nil
		if a.goal == nil || a.goal.Status != "active" || a.goal.Waiting == nil {
			return map[string]any{}, nil
		}
		g := *a.goal
		reason := g.Waiting.Reason
		a.goal.Waiting = nil
		a.goal.UpdatedAt = time.Now().UnixMilli()
		if err := a.persist(ctx); err != nil {
			return nil, err
		}
		return map[string]any{}, a.enqueue(ctx, prompt("waiting", a.goal, "", reason), fmt.Sprintf("goal-wait-resume:%s:%d", g.ID, g.UpdatedAt), "settled")
	case "_goal.restoreContinue":
		if a.goal == nil || a.goal.Status != "active" || a.goal.Waiting != nil {
			return map[string]any{}, nil
		}
		var snap map[string]any
		if err := a.host(ctx, "session.snapshot", map[string]any{}, &snap); err != nil {
			return nil, err
		}
		if snap["running"] == true || snap["idle"] == false {
			return map[string]any{}, nil
		}
		return map[string]any{}, a.continueGoal(ctx)
	case "command.invoke":
		if stringValue(p["name"]) != "goal" {
			return commandResult{Handled: false}, nil
		}
		return a.command(ctx, text(p["args"]))
	case "lifecycle.invoke":
		if stringValue(p["event"]) == "before_agent_start" && a.goal != nil && a.goal.Status == "active" {
			system, _ := record(p["payload"])["system"].(string)
			return map[string]any{"system": appendSystem(system, prompt("system", a.goal, "", ""))}, nil
		}
	case "lifecycle.event":
		if stringValue(p["event"]) == "agent_settled" {
			return map[string]any{}, a.continueGoal(ctx)
		}
	case "tool.execute":
		return a.execute(ctx, stringValue(p["name"]), record(p["args"]))
	case "ui.action":
		action := stringValue(p["id"])
		if action == "clear" {
			var result struct {
				OK bool `json:"ok"`
			}
			message := any(ui("noActiveGoal"))
			if a.goal != nil {
				message = a.goal.Text
			}
			if a.host(ctx, "ui.confirm", map[string]any{"title": ui("confirmClear"), "message": message}, &result) != nil || !result.OK {
				return map[string]any{}, nil
			}
		}
		if action == "pause" || action == "resume" || action == "clear" {
			out, err := a.applyCommand(ctx, action, "")
			if err != nil {
				return nil, err
			}
			if out.Prompt != "" {
				return map[string]any{}, a.enqueue(ctx, out.Prompt, "goal-resume:"+a.goal.ID, "now")
			}
		}
	case "ui.submit":
		objective := text(record(p["fields"])["objective"])
		if objective != "" {
			kind := "edit"
			if a.goal == nil || a.goal.Status == "complete" {
				kind = "start"
			}
			out, err := a.applyCommand(ctx, kind, objective)
			if err != nil {
				return nil, err
			}
			if out.Prompt != "" {
				return map[string]any{}, a.enqueue(ctx, out.Prompt, "goal-form:"+a.goal.ID, "now")
			}
		}
	}
	return map[string]any{}, nil
}
func (s *sidecar) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.apps {
		a.close()
	}
}
func main() {
	peer := extensionrpc.New(os.Stdout)
	s := &sidecar{apps: map[string]*app{}, home: os.Getenv("KI_HOME"), call: func(ctx context.Context, method string, p map[string]any, out any) error {
		return peer.Call(ctx, method, p, out)
	}}
	if err := peer.Serve(context.Background(), os.Stdin, s.handle); err != nil {
		fmt.Fprintln(os.Stderr, "goal:", err)
	}
	s.close()
}
