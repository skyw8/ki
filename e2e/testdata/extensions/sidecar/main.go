package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func reply(id any, result any) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func sendUI(sessionID string) {
	if os.Getenv("KI_SET_UI") != "1" || sessionID == "" {
		return
	}
	text := os.Getenv("KI_STATUS_TEXT")
	if text == "" {
		text = "Goal · active"
	}
	tone := os.Getenv("KI_STATUS_TONE")
	if tone == "" {
		tone = "active"
	}
	title := os.Getenv("KI_PANEL_TITLE")
	if title == "" {
		title = "Goal"
	}
	summary := os.Getenv("KI_PANEL_SUMMARY")
	if summary == "" {
		summary = "fixture panel text"
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": "ui-status", "method": "ui.setStatus",
		"params": map[string]any{"sessionId": sessionID, "key": "goal", "text": text, "tone": tone},
	})
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": "ui-panel", "method": "ui.setPanel",
		"params": map[string]any{
			"sessionId": sessionID, "title": title, "summary": summary,
			"sections": []any{map[string]any{
				"heading": "Details",
				"items":   []any{map[string]any{"label": "Status", "value": text}},
			}},
			"fields": []any{map[string]any{
				"id": "note", "label": "Note", "type": "textarea", "value": summary,
			}},
			"submitLabel": "Update",
			"actions":     []any{map[string]any{"id": "ack", "label": "Ack"}},
		},
	})
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	uiSent := map[string]bool{}
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			if p := os.Getenv("KI_MARKER"); p != "" {
				_ = os.WriteFile(p, []byte("1"), 0o600)
			}
			if ms := os.Getenv("KI_INIT_SLEEP_MS"); ms != "" {
				n, _ := strconv.Atoi(ms)
				if n > 0 {
					time.Sleep(time.Duration(n) * time.Millisecond)
				}
			}
			var p struct {
				Capabilities []string `json:"capabilities"`
				SessionID    string   `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			result := map[string]any{"tools": []any{}, "commands": []any{}}
			if hasCap(p.Capabilities, "lifecycle") {
				subs := []any{
					map[string]any{"event": "tool_call", "mode": "sync"},
					map[string]any{"event": "agent_end", "mode": "async"},
					map[string]any{"event": "agent_settled", "mode": "async"},
				}
				if os.Getenv("KI_REWRITE_INPUT") != "" || os.Getenv("KI_SWALLOW_INPUT") == "1" {
					subs = append(subs, map[string]any{"event": "input", "mode": "sync"})
				}
				if os.Getenv("KI_COMPACT_CANCEL") == "1" || os.Getenv("KI_COMPACT_SUMMARY") != "" {
					subs = append(subs, map[string]any{"event": "session_before_compact", "mode": "sync"})
				}
				result["subscriptions"] = subs
			}
			if hasCap(p.Capabilities, "command") {
				result["commands"] = []any{
					map[string]any{"name": "ship", "description": "extension ship"},
					map[string]any{"name": "extnotice", "description": "notice only"},
					map[string]any{"name": "extprompt", "description": "prompt occupy"},
				}
			}
			if os.Getenv("KI_COMPLETIONS") == "1" {
				result["commands"] = []any{
					map[string]any{
						"name": "goal", "description": "Run a goal to completion",
						"argumentHint": "<objective>",
						"completions":  []string{"pause", "resume", "clear", "edit", "status"},
					},
				}
			}
			if os.Getenv("KI_UNDECLARED") == "1" {
				result["tools"] = []any{map[string]any{"name": "sneaky", "description": "x", "parameters": map[string]any{"type": "object"}}}
				result["commands"] = []any{map[string]any{"name": "sneaky"}}
			}
			reply(m.ID, result)
			// Global sidecars learn the target session from the lifecycle RPC.
		case "command.invoke":
			var p struct {
				Name string `json:"name"`
				Args string `json:"args"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if f := os.Getenv("KI_INVOKE_MARKER"); f != "" {
				_ = os.WriteFile(f, []byte(p.Name), 0o600)
			}
			switch p.Name {
			case "extnotice":
				reply(m.ID, map[string]any{"handled": true, "notice": "SHIP-NOTICE"})
			case "ship", "extprompt":
				reply(m.ID, map[string]any{"handled": false, "prompt": "SHIP-PROMPT"})
			default:
				reply(m.ID, map[string]any{"handled": false})
			}
		case "lifecycle.invoke":
			var wrap struct {
				Event     string          `json:"event"`
				Payload   json.RawMessage `json:"payload"`
				SessionID string          `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &wrap)
			if !uiSent[wrap.SessionID] {
				sendUI(wrap.SessionID)
				uiSent[wrap.SessionID] = true
			}
			if wrap.Event == "input" {
				if os.Getenv("KI_SWALLOW_INPUT") == "1" {
					reply(m.ID, map[string]any{"swallow": true})
					continue
				}
				if next := os.Getenv("KI_REWRITE_INPUT"); next != "" {
					reply(m.ID, map[string]any{"text": next})
					continue
				}
			}
			if wrap.Event == "session_before_compact" {
				if os.Getenv("KI_COMPACT_CANCEL") == "1" {
					reply(m.ID, map[string]any{"cancel": true})
					continue
				}
				if sum := os.Getenv("KI_COMPACT_SUMMARY"); sum != "" {
					reply(m.ID, map[string]any{"summary": sum})
					continue
				}
			}
			if wrap.Event == "tool_call" {
				var p struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				}
				_ = json.Unmarshal(wrap.Payload, &p)
				path, _ := p.Args["path"].(string)
				cmd, _ := p.Args["command"].(string)
				if p.Name == "Write" && strings.Contains(path, ".env") {
					reply(m.ID, map[string]any{"block": true, "reason": "blocked .env"})
					continue
				}
				if p.Name == "Bash" && strings.Contains(cmd, "SLEEP_INTERCEPT") {
					c := exec.Command("sleep", "30")
					_ = c.Start()
					if f := os.Getenv("KI_GRANDCHILD_PID_FILE"); f != "" && c.Process != nil {
						_ = os.WriteFile(f, []byte(strconv.Itoa(c.Process.Pid)), 0o600)
					}
					time.Sleep(30 * time.Second)
				}
			}
			reply(m.ID, map[string]any{})
		case "lifecycle.event":
			var event struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &event)
			if !uiSent[event.SessionID] {
				sendUI(event.SessionID)
				uiSent[event.SessionID] = true
			}
		case "session.open":
			var open struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &open)
			if !uiSent[open.SessionID] {
				sendUI(open.SessionID)
				uiSent[open.SessionID] = true
			}
		case "ui.action", "ui.submit":
			if f := os.Getenv("KI_UI_MARKER"); f != "" {
				_ = os.WriteFile(f, []byte(m.Method), 0o600)
			}
			if m.ID != nil {
				reply(m.ID, map[string]any{})
			}
		default:
			if m.ID != nil {
				reply(m.ID, map[string]any{})
			}
		}
	}
}
