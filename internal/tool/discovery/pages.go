package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type discoveryRequest struct {
	query  string
	limit  int
	name   string
	cursor string
}

type schemaPage struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"total_bytes"`
	Fragment   string `json:"fragment"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func parseRequest(args map[string]any) (discoveryRequest, error) {
	if _, exists := args["name"]; !exists {
		query, limit, err := searchArgs(args)
		return discoveryRequest{query: query, limit: limit}, err
	}
	for key := range args {
		if key != "name" && key != "cursor" {
			return discoveryRequest{}, fmt.Errorf("name/cursor cannot be combined with %q", key)
		}
	}
	name, ok := args["name"].(string)
	if !ok || len(name) > 256 {
		return discoveryRequest{}, fmt.Errorf("name must be an exact tool name of at most 256 bytes")
	}
	canonical, err := toolapi.Canonical(name)
	if err != nil || canonical != name {
		return discoveryRequest{}, fmt.Errorf("name must be an exact canonical tool name")
	}
	req := discoveryRequest{name: name}
	if value, exists := args["cursor"]; exists {
		cursor, ok := value.(string)
		if !ok {
			return discoveryRequest{}, fmt.Errorf("cursor must be a declaration page cursor")
		}
		if _, _, err := decodeCursor(cursor); err != nil {
			return discoveryRequest{}, err
		}
		req.cursor = cursor
	}
	return req, nil
}

func decodeCursor(cursor string) (string, int, error) {
	digest, position, ok := strings.Cut(cursor, ":")
	if !ok || len(digest) != sha256.Size*2 || len(position) > 12 {
		return "", 0, fmt.Errorf("invalid declaration page cursor")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", 0, fmt.Errorf("invalid declaration page cursor")
	}
	offset, err := strconv.Atoi(position)
	if err != nil || offset <= 0 || strconv.Itoa(offset) != position {
		return "", 0, fmt.Errorf("invalid declaration page cursor")
	}
	return digest, offset, nil
}

func declarationDigest(raw json.RawMessage) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (c *Catalog) page(req discoveryRequest) (schemaPage, error) {
	if c == nil {
		return schemaPage{}, fmt.Errorf("tool %q is not available", req.name)
	}
	index, exists := c.byName[req.name]
	if !exists {
		return schemaPage{}, fmt.Errorf("tool %q is not available", req.name)
	}
	e := c.entries[index]
	raw := e.raw
	digest := e.digest
	offset := 0
	if req.cursor != "" {
		var requestedDigest string
		var err error
		requestedDigest, offset, err = decodeCursor(req.cursor)
		if err != nil {
			return schemaPage{}, err
		}
		if requestedDigest != digest {
			return schemaPage{}, fmt.Errorf("tool declaration changed; restart with name and no cursor")
		}
		if offset >= len(raw) || !utf8.RuneStart(raw[offset]) {
			return schemaPage{}, fmt.Errorf("declaration page cursor is outside a UTF-8 boundary")
		}
	}
	page := schemaPage{Name: req.name, Digest: digest, Offset: offset, TotalBytes: len(raw)}
	end := min(len(raw), offset+maxResultBytes-1024)
	for {
		for end > offset && end < len(raw) && !utf8.RuneStart(raw[end]) {
			end--
		}
		page.Fragment = string(raw[offset:end])
		page.NextCursor = ""
		if end < len(raw) {
			page.NextCursor = digest + ":" + strconv.Itoa(end)
		}
		encoded, err := json.Marshal(map[string]any{"schema_page": page})
		if err != nil {
			return schemaPage{}, err
		}
		if len(encoded) <= maxResultBytes {
			return page, nil
		}
		end = offset + (end-offset)/2
		if end <= offset {
			return schemaPage{}, fmt.Errorf("tool declaration page metadata exceeds output budget")
		}
	}
}

func (c *Catalog) fetchPage(req discoveryRequest) toolapi.Result {
	page, err := c.page(req)
	if err != nil {
		return toolapi.Result{IsError: true, Content: []types.Content{{Type: "text", Text: err.Error()}}}
	}
	raw, _ := json.Marshal(map[string]any{"schema_page": page})
	return toolapi.Result{Content: []types.Content{{Type: "text", Text: string(raw)}}}
}

func (c *Catalog) disclosedPage(req discoveryRequest, text string) (schemaPage, bool) {
	if len(text) > maxResultBytes {
		return schemaPage{}, false
	}
	var body struct {
		Page *schemaPage `json:"schema_page"`
	}
	if json.Unmarshal([]byte(text), &body) != nil || body.Page == nil {
		return schemaPage{}, false
	}
	expected, err := c.page(req)
	return expected, err == nil && *body.Page == expected
}
