package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/tool"
	"ki/internal/types"
)

type adapter struct {
	manager     *Manager
	session     *sdk.ClientSession
	server      string
	raw         string
	name        string
	description string
	source      string
	parameters  map[string]any
	validator   *jsonschema.Resolved
	timeout     time.Duration
	schemaBytes int
}

func newAdapter(m *Manager, session *sdk.ClientSession, server, source string, timeout time.Duration, remote *sdk.Tool) (*adapter, error) {
	data, err := json.Marshal(remote.InputSchema)
	if err != nil {
		return nil, err
	}
	if len(data) > 256*1024 {
		return nil, errors.New("input schema exceeds 256KiB")
	}
	var parameters map[string]any
	if err := json.Unmarshal(data, &parameters); err != nil || parameters == nil {
		return nil, errors.New("input schema must be a JSON object")
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	// A nil loader rejects remote references instead of performing network or
	// filesystem access while resolving an untrusted tool's input schema.
	validator, err := schema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	return &adapter{
		manager: m, session: session, server: server, raw: remote.Name,
		name: modelName(server, remote.Name), description: boundedText(remote.Description, 64*1024),
		source: source, parameters: parameters, validator: validator, timeout: timeout, schemaBytes: len(data),
	}, nil
}

func (t *adapter) Name() string                 { return t.name }
func (t *adapter) Description() string          { return t.description }
func (t *adapter) Prompt() string               { return "" }
func (t *adapter) Snippet() string              { return "" }
func (t *adapter) SourceInfo() (string, string) { return t.server, t.source }
func (t *adapter) Parameters() map[string]any {
	// Callers may render/augment schemas. Never expose the frozen catalog map.
	data, _ := json.Marshal(t.parameters)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
func (t *adapter) Validate(args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("MCP arguments: %w", err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return t.validator.Validate(value)
}

func (t *adapter) Execute(ctx context.Context, args map[string]any) tool.Result {
	if err := t.Validate(args); err != nil {
		return failed(err)
	}
	ctx, done, err := t.manager.begin(ctx)
	if err != nil {
		return failed(err)
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	result, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: t.raw, Arguments: args})
	if err != nil {
		return failed(fmt.Errorf("MCP %s/%s: %w", t.server, t.raw, err))
	}
	if result == nil {
		return failed(errors.New("MCP server returned no result"))
	}
	return projectResult(t.server, t.raw, result)
}

func failed(err error) tool.Result {
	return tool.Result{IsError: true, Content: []types.Content{{Type: "text", Text: err.Error()}}}
}

func projectResult(server, raw string, result *sdk.CallToolResult) tool.Result {
	data, err := json.Marshal(result)
	if err != nil {
		return failed(fmt.Errorf("encode MCP result: %w", err))
	}
	if len(data) > 8*1024*1024 {
		return failed(errors.New("MCP result exceeds 8MiB"))
	}
	var details map[string]any
	if err := json.Unmarshal(data, &details); err != nil {
		return failed(err)
	}
	details["_mcp"] = map[string]any{"server": server, "tool": raw}
	out := tool.Result{IsError: result.IsError, Details: details}
	content, _ := details["content"].([]any)
	// Content is the sole copy of model-facing payloads. Keeping raw MCP text
	// or media in Details would bypass an AfterTool redaction of Content.
	delete(details, "content")
	var metadata []any
	for _, rawBlock := range content {
		block, _ := rawBlock.(map[string]any)
		kind, _ := block["type"].(string)
		metadata = append(metadata, map[string]any{"type": kind, "_meta": block["_meta"], "annotations": block["annotations"]})
		switch kind {
		case "text":
			text, _ := block["text"].(string)
			out.Content = append(out.Content, types.Content{Type: "text", Text: text})
		case "image", "audio":
			data, _ := block["data"].(string)
			mime, _ := block["mimeType"].(string)
			out.Content = append(out.Content, types.Content{Type: kind, Data: data, MIMEType: mime})
		default:
			// Resources and links remain inert data, never ambient capabilities.
			text, _ := json.Marshal(rawBlock)
			out.Content = append(out.Content, types.Content{Type: "text", Text: string(text)})
		}
	}
	if len(metadata) > 0 {
		details["contentMetadata"] = metadata
	}
	if len(out.Content) == 0 {
		if structured := details["structuredContent"]; structured != nil {
			text, _ := json.Marshal(structured)
			out.Content = []types.Content{{Type: "text", Text: string(text)}}
		}
	}
	return out
}

func modelName(server, raw string) string {
	s := normalizePart(server)
	r := normalizePart(raw)
	name := "mcp__" + s + "__" + r
	if s != server || r != raw || len(name) > 128 {
		return hashedName(name, server, raw)
	}
	return name
}

func normalizePart(raw string) string {
	var b strings.Builder
	for _, c := range raw {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

func hashedName(base, server, raw string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + raw))
	suffix := "__" + hex.EncodeToString(sum[:8])
	if len(base) > 128-len(suffix) {
		base = base[:128-len(suffix)]
	}
	return base + suffix
}

func normalizeCatalogNames(adapters []*adapter) []tool.Tool {
	aliases := make(map[string][]int)
	for i, t := range adapters {
		names, _ := tool.Aliases(t.name)
		for _, alias := range names {
			aliases[alias] = append(aliases[alias], i)
		}
	}
	collisions := make(map[int]bool)
	for _, indexes := range aliases {
		if len(indexes) > 1 {
			for _, i := range indexes {
				collisions[i] = true
			}
		}
	}
	out := make([]tool.Tool, len(adapters))
	for i, t := range adapters {
		copy := *t
		if collisions[i] {
			copy.name = hashedName(t.name, t.server, t.raw)
		}
		out[i] = &copy
	}
	slices.SortFunc(out, func(a, b tool.Tool) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

func boundedText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	text = text[:maxBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
