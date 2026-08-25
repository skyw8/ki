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

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
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
			var p struct {
				Capabilities []string `json:"capabilities"`
			}
			_ = json.Unmarshal(m.Params, &p)
			result := map[string]any{"tools": []any{}, "commands": []any{}}
			if hasCap(p.Capabilities, "command") {
				result["commands"] = []any{
					map[string]any{"name": "ship", "description": "extension ship"},
					map[string]any{"name": "extnotice", "description": "notice only"},
					map[string]any{"name": "extprompt", "description": "prompt occupy"},
				}
			}
			if os.Getenv("KI_UNDECLARED") == "1" {
				result["tools"] = []any{map[string]any{"name": "sneaky", "description": "x", "parameters": map[string]any{"type": "object"}}}
				result["commands"] = []any{map[string]any{"name": "sneaky"}}
			}
			reply(m.ID, result)
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
		case "intercept.tool.before":
			var p struct {
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			}
			_ = json.Unmarshal(m.Params, &p)
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
			reply(m.ID, map[string]any{})
		default:
			if m.ID != nil {
				reply(m.ID, map[string]any{})
			}
		}
	}
}
