package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

const (
	Name           = "search_tool"
	defaultLimit   = 8
	maxLimit       = 32
	maxQueryBytes  = 4096
	maxResultBytes = 12 * 1024
	maxIndexBytes  = 64 * 1024
	maxSourceBytes = 4096
)

// SourceInfo optionally describes a tool's server or extension without exposing
// the server's complete tool inventory in the initial model request.
type SourceInfo interface {
	SourceInfo() (name, description string)
}

type entry struct {
	name   string
	tool   toolapi.Tool
	raw    json.RawMessage
	length int
}

type posting struct {
	index     int
	frequency int
}

// Catalog owns frozen schemas for tools already admitted by the host's policy.
type Catalog struct {
	entries       []entry
	byName        map[string]int
	index         map[string][]posting
	averageLength float64
	sources       string
}

// New builds a catalog of deferred tools, not the whole execution registry.
func New(tools []toolapi.Tool) *Catalog {
	c := &Catalog{byName: map[string]int{}, index: map[string][]posting{}}
	sources := map[string]string{}
	for _, t := range tools {
		if t == nil {
			continue
		}
		name, err := toolapi.Canonical(t.Name())
		if err != nil || name == Name || name == "exec" || name == "wait" {
			continue
		}
		if _, exists := c.byName[name]; exists {
			continue
		}
		spec := toolapi.SpecFor(t)
		raw, err := json.Marshal(spec)
		if err != nil {
			continue
		}
		// Freeze nested schema maps so caller mutation cannot change the index or
		// turn a loaded historical ID into a different declaration mid-occupy.
		if json.Unmarshal(raw, &spec) != nil {
			continue
		}
		source, description := "Other tools", ""
		if info, ok := t.(SourceInfo); ok {
			source, description = info.SourceInfo()
			source, description = strings.TrimSpace(source), strings.TrimSpace(description)
			if source == "" {
				source = "Other tools"
			}
		}
		if old := sources[source]; old == "" {
			sources[source] = description
		}
		text := name + " " + strings.ReplaceAll(name, "_", " ") + " " + source + " " + description + " " + spec.Description
		if parameters, err := json.Marshal(spec.Parameters); err == nil {
			text += " " + string(parameters)
		}
		frequencies := map[string]int{}
		tokens := tokenize(boundText(text, maxIndexBytes))
		for _, token := range tokens {
			frequencies[token]++
		}
		idx := len(c.entries)
		c.byName[name] = idx
		c.entries = append(c.entries, entry{name: name, tool: t, raw: raw, length: len(tokens)})
		for token, frequency := range frequencies {
			c.index[token] = append(c.index[token], posting{index: idx, frequency: frequency})
		}
		c.averageLength += float64(len(tokens))
	}
	if len(c.entries) > 0 {
		c.averageLength /= float64(len(c.entries))
	}
	c.sources = renderSources(sources)
	return c
}

// Names returns the current canonical deferred identities in stable order.
func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		names = append(names, e.name)
	}
	slices.Sort(names)
	return names
}

// SourceSummary lists sources, not individual tools or schemas.
func (c *Catalog) SourceSummary() string {
	if c == nil {
		return ""
	}
	return c.sources
}

func renderSources(sources map[string]string) string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		line := "\n- " + boundText(name, 128)
		if description := sources[name]; description != "" {
			line += ": " + boundText(description, 256)
		}
		if b.Len()+len(line) > maxSourceBytes-64 {
			b.WriteString("\n- Additional sources omitted.")
			break
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "None currently available."
	}
	return strings.TrimSpace(b.String())
}

func boundText(s string, bytes int) string {
	if len(s) <= bytes {
		return s
	}
	// Keep UTF-8 intact at all metadata boundaries.
	for bytes > 0 && bytes < len(s) && s[bytes]&0xc0 == 0x80 {
		bytes--
	}
	return s[:bytes] + "…"
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

type ranked struct {
	index int
	score float64
}

func (c *Catalog) search(query string, limit int) []entry {
	if c == nil || len(c.entries) == 0 {
		return nil
	}
	scores := map[int]float64{}
	seen := map[string]bool{}
	for _, token := range tokenize(query) {
		if seen[token] {
			continue
		}
		seen[token] = true
		postings := c.index[token]
		if len(postings) == 0 {
			continue
		}
		idf := math.Log(1 + (float64(len(c.entries)-len(postings))+.5)/(float64(len(postings))+.5))
		for _, p := range postings {
			length := float64(c.entries[p.index].length)
			frequency := float64(p.frequency)
			denominator := frequency + 1.2*(1-.75+.75*length/max(c.averageLength, 1))
			scores[p.index] += idf * frequency * 2.2 / denominator
		}
	}
	order := make([]ranked, 0, len(scores))
	for index, score := range scores {
		order = append(order, ranked{index, score})
	}
	slices.SortFunc(order, func(a, b ranked) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		return strings.Compare(c.entries[a.index].name, c.entries[b.index].name)
	})
	results := make([]entry, 0, min(limit, len(order)))
	for _, r := range order[:min(limit, len(order))] {
		results = append(results, c.entries[r.index])
	}
	return results
}

type searchTool struct{ catalog *Catalog }

func (c *Catalog) SearchTool() toolapi.Tool { return &searchTool{catalog: c} }
func (*searchTool) Name() string            { return Name }
func (*searchTool) Description() string {
	return "Find deferred tools by searching their names, descriptions, sources and parameters"
}
func (*searchTool) Snippet() string {
	return "Search tool metadata and disclose selected input schemas"
}
func (t *searchTool) Prompt() string {
	return "Search deferred tool metadata with lexical BM25. Use query and optional limit (default 8, maximum 32). Returned schemas become available on the next model request. This discovers tools, not files or MCP resources, and does not grant execution permissions. Code Mode can already use allowed tools through tools and ALL_TOOLS. Sources:\n" + t.catalog.SourceSummary()
}
func (*searchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query": map[string]any{"type": "string", "description": "Tool discovery query", "maxLength": maxQueryBytes},
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxLimit},
	}, "required": []any{"query"}, "additionalProperties": false}
}

func searchArgs(args map[string]any) (string, int, error) {
	for key := range args {
		if key != "query" && key != "limit" {
			return "", 0, fmt.Errorf("unknown search_tool argument %q", key)
		}
	}
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" || len(query) > maxQueryBytes {
		return "", 0, fmt.Errorf("query must be a non-empty string of at most %d UTF-8 bytes", maxQueryBytes)
	}
	limit := defaultLimit
	if value, exists := args["limit"]; exists {
		var err error
		switch v := value.(type) {
		case int:
			limit = v
		case int64:
			if v < 1 || v > maxLimit {
				err = fmt.Errorf("out of range")
			} else {
				limit = int(v)
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v < 1 || v > maxLimit {
				err = fmt.Errorf("out of range")
			} else {
				limit = int(v)
			}
		case json.Number:
			n, e := v.Int64()
			if e != nil || n < 1 || n > maxLimit {
				err = fmt.Errorf("out of range")
			} else {
				limit = int(n)
			}
		default:
			err = fmt.Errorf("invalid type")
		}
		if err != nil || limit < 1 || limit > maxLimit {
			return "", 0, fmt.Errorf("limit must be an integer from 1 to %d", maxLimit)
		}
	}
	return strings.TrimSpace(query), limit, nil
}
func (*searchTool) Validate(args map[string]any) error {
	_, _, err := searchArgs(args)
	return err
}

func (t *searchTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	query, limit, err := searchArgs(args)
	if err != nil {
		return toolapi.Result{IsError: true, Content: []types.Content{{Type: "text", Text: err.Error()}}}
	}
	if err := ctx.Err(); err != nil {
		return toolapi.Result{IsError: true, Content: []types.Content{{Type: "text", Text: err.Error()}}}
	}
	matches := t.catalog.search(query, limit)
	var tools []json.RawMessage
	names := []string{}
	omitted := []map[string]string{}
	bytes := 0
	for _, e := range matches {
		// Never shorten an input schema: a partial declaration changes argument
		// semantics. Explicit omissions do not enter the loaded identity list.
		if bytes+len(e.raw) > maxResultBytes-2048 {
			omitted = append(omitted, map[string]string{"name": boundText(e.name, 128), "reason": "complete schema exceeds discovery output budget"})
			continue
		}
		tools = append(tools, e.raw)
		names = append(names, e.name)
		bytes += len(e.raw) + 1
	}
	if tools == nil {
		tools = []json.RawMessage{}
	}
	body := map[string]any{"tools": tools}
	if len(omitted) > 0 {
		// Reserve the final bytes for a bounded omission notice, not another schema.
		notice, _ := json.Marshal(omitted)
		if len(notice) <= 1800 {
			body["omitted_tools"] = omitted
		} else {
			body["omitted_tools"] = "Some matches omitted: complete schemas exceed discovery output budget."
		}
	}
	raw, _ := json.Marshal(body)
	return toolapi.Result{Content: []types.Content{{Type: "text", Text: string(raw)}}, Details: map[string]any{"discovered_tools": names}}
}

// Loaded extracts accepted discovery identities from actual model-facing
// history. Pairing consumes each search call once, including failed results.
func (c *Catalog) Loaded(history []types.Message) []toolapi.Tool {
	if c == nil {
		return nil
	}
	pending := map[string]bool{}
	seenCalls := map[string]bool{}
	loaded := map[string]bool{}
	for _, message := range history {
		if message.Role == "assistant" {
			for _, call := range message.Content {
				if call.Type != "toolCall" || call.ID == "" {
					continue
				}
				pending[call.ID] = false
				if seenCalls[call.ID] {
					continue
				}
				seenCalls[call.ID] = true
				if name, err := toolapi.Canonical(call.Name); err == nil && name == Name && message.StopReason != "error" {
					_, _, err := searchArgs(call.Arguments)
					pending[call.ID] = err == nil
				}
			}
			continue
		}
		if message.Role != "toolResult" {
			continue
		}
		valid, exists := pending[message.ToolCallID]
		delete(pending, message.ToolCallID)
		name, err := toolapi.Canonical(message.ToolName)
		if !exists || !valid || err != nil || name != Name || message.IsError {
			continue
		}
		raw, err := json.Marshal(message.Details)
		if err != nil {
			continue
		}
		var details struct {
			Names []string `json:"discovered_tools"`
		}
		if json.Unmarshal(raw, &details) != nil || len(details.Names) > maxLimit {
			continue
		}
		proven := c.disclosedSchemas(message.Text())
		for _, requested := range details.Names {
			name, err := toolapi.Canonical(requested)
			if err == nil && proven[name] {
				if _, exists := c.byName[name]; exists {
					loaded[name] = true
				}
			}
		}
	}
	names := make([]string, 0, len(loaded))
	for name := range loaded {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]toolapi.Tool, 0, len(names))
	for _, name := range names {
		e := c.entries[c.byName[name]]
		result = append(result, publishedTool{Tool: e.tool, raw: e.raw})
	}
	return result
}

// disclosedSchemas proves that the post-hook body actually disclosed the
// complete current schema. Details alone cannot resurrect content redacted by
// output policy; stale or truncated bodies require discovery again.
func (c *Catalog) disclosedSchemas(text string) map[string]bool {
	if len(text) > maxResultBytes {
		return nil
	}
	var body struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || len(body.Tools) > maxLimit {
		return nil
	}
	proven := map[string]bool{}
	for _, raw := range body.Tools {
		var identity struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &identity) != nil {
			continue
		}
		index, exists := c.byName[identity.Name]
		if !exists {
			continue
		}
		var actual, expected any
		if json.Unmarshal(raw, &actual) == nil && json.Unmarshal(c.entries[index].raw, &expected) == nil && reflect.DeepEqual(actual, expected) {
			proven[identity.Name] = true
		}
	}
	return proven
}

// publishedTool delegates behavior but advertises the occupy's frozen schema.
// Persisted search output schemas never replace the current catalog declaration.
type publishedTool struct {
	toolapi.Tool
	raw json.RawMessage
}

func (t publishedTool) ToolSpec() toolapi.Spec {
	var spec toolapi.Spec
	_ = json.Unmarshal(t.raw, &spec)
	return spec
}
