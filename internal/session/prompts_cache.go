package session

import (
	"crypto/sha256"
	"encoding/json"
)

// promptPool belongs to one decoded snapshot/Session, never to the process.
// Why: a global intern table would retain prompts after body-cache eviction.
type promptPool struct {
	systems map[[32]byte]string
	tools   map[[32]byte][]ToolSchema
}

func (p *promptPool) system(s string) string {
	if s == "" {
		return ""
	}
	if p.systems == nil {
		p.systems = map[[32]byte]string{}
	}
	key := sha256.Sum256([]byte(s))
	if old, ok := p.systems[key]; ok {
		return old
	}
	p.systems[key] = s
	return s
}

func (p *promptPool) schemas(raw []byte) ([]ToolSchema, error) {
	key := sha256.Sum256(raw)
	if tools, ok := p.tools[key]; ok {
		return tools, nil
	}
	var tools []ToolSchema
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, err
		}
	}
	if p.tools == nil {
		p.tools = map[[32]byte][]ToolSchema{}
	}
	p.tools[key] = tools
	return tools, nil
}

func decodeEntry(raw []byte, pool *promptPool) (Entry, error) {
	// Override tools while decoding the rest of Entry, so repeated schemas
	// never allocate thousands of maps just to discard them after interning.
	type wireEntry Entry
	var e Entry
	wire := struct {
		*wireEntry
		Tools json.RawMessage `json:"tools"`
	}{wireEntry: (*wireEntry)(&e)}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Entry{}, err
	}
	var err error
	e.System = pool.system(e.System)
	e.Tools, err = pool.schemas(wire.Tools)
	return e, err
}
