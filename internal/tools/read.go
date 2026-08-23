package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"ki/internal/loop"
	"ki/internal/types"
)

type readTool struct {
	cwd  string
	rich bool
	ops  ReadOperations
}

// ReadOperations is replaceable so Read can target a remote or virtual
// filesystem without changing its model-visible contract.
type ReadOperations interface {
	Stat(context.Context, string) (os.FileInfo, error)
	ReadFile(context.Context, string) ([]byte, error)
}

type localReadOperations struct{}

func (localReadOperations) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return info, err
}
func (localReadOperations) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return b, err
}

type truncationDetails struct {
	Truncated      bool   `json:"truncated"`
	Reason         string `json:"reason,omitempty"`
	TotalBytes     int    `json:"total_bytes"`
	TotalLines     int    `json:"total_lines"`
	OutputBytes    int    `json:"output_bytes"`
	OutputLines    int    `json:"output_lines"`
	NextOffset     int    `json:"next_offset,omitempty"`
	NextByteOffset int    `json:"next_byte_offset,omitempty"`
}

type readDetails struct {
	Truncation *truncationDetails `json:"truncation,omitempty"`
	Image      *imageDetails      `json:"image,omitempty"`
}

func (readTool) Name() string        { return "Read" }
func (readTool) Description() string { return "Read a file from the local filesystem." }
func (readTool) Snippet() string     { return "Read file contents" }
func (t readTool) Prompt() string {
	base := `Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters`
	if t.rich {
		base += `
- This tool allows reading images (eg PNG, JPG, etc).
- This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges (e.g., pages: "1-5"). Maximum 20 pages per request.`
	}
	return base + `
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs, combining code, text, and visualizations.
- This tool can only read files, not directories. To read a directory, use an ls command via the Bash tool.`
}

func (t readTool) Parameters() map[string]any {
	properties := map[string]any{
		"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to read"},
		"offset":      map[string]any{"type": "integer", "description": "The line number to start reading from. Only provide if the file is too large to read at once"},
		"limit":       map[string]any{"type": "integer", "description": "The number of lines to read. Only provide if the file is too large to read at once."},
		"byte_offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based byte position for continuing a line larger than the normal output limit. Cannot be combined with offset or limit."},
		"byte_limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxBytes, "description": "Maximum bytes to return in byte mode. Defaults to 51200. Cannot be combined with offset or limit."},
	}
	if t.rich {
		properties["pages"] = map[string]any{"type": "string", "description": "Page range for PDF files (e.g., \"1-5\", \"3\", \"10-20\"). Only applicable to PDF files. Maximum 20 pages per request."}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path"},
		"properties":           properties,
	}
}

// Validate checks the model-supplied arguments against the tool schema before
// any execution (pi validateToolArguments; loop ToolValidator).
func (t readTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t readTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	if path == "" {
		return errRes("file_path is required")
	}
	abs := resolve(t.cwd, path)
	if _, byteMode := args["byte_offset"]; byteMode {
		if _, ok := args["offset"]; ok {
			return errRes("byte_offset cannot be combined with offset or limit")
		}
		if _, ok := args["limit"]; ok {
			return errRes("byte_offset cannot be combined with offset or limit")
		}
	} else if _, ok := args["byte_limit"]; ok {
		if _, lineOffset := args["offset"]; lineOffset {
			return errRes("byte_limit cannot be combined with offset or limit")
		}
		if _, lineLimit := args["limit"]; lineLimit {
			return errRes("byte_limit cannot be combined with offset or limit")
		}
	}
	ops := t.ops
	if ops == nil {
		ops = localReadOperations{}
	}
	st, err := ops.Stat(ctx, abs)
	if err != nil {
		return errRes(err.Error())
	}
	if st.IsDir() {
		return errRes("This tool can only read files, not directories. Use Bash ls.")
	}
	//nolint:gosec // abs is normalized and confined to the requested tool path.
	data, err := ops.ReadFile(ctx, abs)
	if err != nil {
		return errRes(err.Error())
	}
	if mime := imageMIME(data); mime != "" {
		if !t.rich {
			// The execution guard matters for stale calls from a previous model
			// turn: schema hiding alone cannot prevent replayed rich Read args.
			return errRes("image files are unavailable for the selected text-only model")
		}
		processed, outMIME, details, err := resizeImageForModel(data, mime)
		if err != nil {
			return errRes("Could not process image: " + err.Error())
		}
		if err := ctx.Err(); err != nil {
			return errRes("Read aborted")
		}
		return loop.ToolResult{Content: []types.Content{
			{Type: "text", Text: "Read image file [" + outMIME + "]"},
			{Type: "image", Data: base64.StdEncoding.EncodeToString(processed), MIMEType: outMIME},
		}, Details: readDetails{Image: details}}
	}
	if strings.HasSuffix(strings.ToLower(abs), ".ipynb") {
		text := formatNotebook(data)
		text, note := truncateHead(text)
		return txt(text + note)
	}
	if bytes.HasPrefix(data, []byte("%PDF")) {
		if !t.rich {
			return errRes("PDF files are unavailable for the selected text-only model")
		}
		pages, _ := args["pages"].(string)
		text := extractPDFText(data, pages)
		text, note := truncateHead(text)
		return txt(text + note)
	}
	text := string(data)
	if !utf8.ValidString(text) {
		return errRes("binary file; cannot read as text")
	}
	totalLines := strings.Count(text, "\n") + 1
	if byteOffset, byteMode := asInt(args["byte_offset"]); byteMode || args["byte_limit"] != nil {
		if byteOffset < 0 || byteOffset > len(data) {
			return errRes("byte_offset is beyond end of file")
		}
		if byteOffset < len(data) && !utf8.RuneStart(data[byteOffset]) {
			return errRes("byte_offset must be on a UTF-8 character boundary")
		}
		byteLimit := maxBytes
		if v, ok := asInt(args["byte_limit"]); ok {
			byteLimit = min(v, maxBytes)
		}
		end := min(len(data), byteOffset+byteLimit)
		for end > byteOffset && end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		chunk := string(data[byteOffset:end])
		d := &truncationDetails{Truncated: end < len(data), TotalBytes: len(data), TotalLines: totalLines, OutputBytes: end - byteOffset, OutputLines: strings.Count(chunk, "\n") + 1}
		if d.Truncated {
			d.Reason = "bytes"
			d.NextByteOffset = end
			chunk += fmt.Sprintf("\n\n[Showing bytes %d-%d of %d. Use byte_offset=%d to continue.]", byteOffset, end-1, len(data), end)
		}
		return loop.ToolResult{Content: []types.Content{{Type: "text", Text: chunk}}, Details: readDetails{Truncation: d}}
	}
	lines := strings.Split(text, "\n")
	off := 1
	if v, ok := asInt(args["offset"]); ok {
		off = v
		if off == 0 {
			off = 1
		}
	}
	start := max(off-1, 0)
	if start >= len(lines) {
		return errRes(fmt.Sprintf("Offset %d is beyond end of file (%d lines total)", off, len(lines)))
	}
	end := len(lines)
	if v, ok := asInt(args["limit"]); ok && v > 0 {
		if start+v < end {
			end = start + v
		}
	}
	selected := strings.Join(lines[start:end], "\n")
	chunk, note := truncateHead(selected)
	outputLines := strings.Count(chunk, "\n") + 1
	d := &truncationDetails{TotalBytes: len(data), TotalLines: len(lines), OutputBytes: len(chunk), OutputLines: outputLines}
	if note != "" && off > 1 {
		shown := outputLines
		note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", off, off+shown-1, len(lines), off+shown)
		d.Truncated, d.Reason, d.NextOffset = true, "bytes_or_lines", off+shown
	} else if end < len(lines) && note == "" {
		note = fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-end, end+1)
		d.Truncated, d.Reason, d.NextOffset = true, "limit", end+1
	} else if note != "" {
		d.Truncated, d.Reason = true, "bytes_or_lines"
		if outputLines < len(lines)-start {
			d.NextOffset = off + outputLines
		}
		if outputLines == 1 && len(selected) > len(chunk) {
			lineStart := 0
			for i := 0; i < start; i++ {
				lineStart += len(lines[i]) + 1
			}
			d.NextOffset = 0
			d.NextByteOffset = lineStart + len(chunk)
			note = fmt.Sprintf("\n\n[Line %d exceeds the byte limit. Use byte_offset=%d to continue.]", off, d.NextByteOffset)
		}
	}
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: chunk + note}}, Details: readDetails{Truncation: d}}
}
