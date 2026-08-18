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
	jobs *JobStore
}

func (readTool) Name() string        { return "Read" }
func (readTool) Description() string { return "Read a file from the local filesystem." }
func (readTool) Snippet() string     { return "Read file contents" }
func (readTool) Prompt() string {
	return `Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters
- This tool allows reading images (eg PNG, JPG, etc).
- This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges (e.g., pages: "1-5"). Maximum 20 pages per request.
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs, combining code, text, and visualizations.
- This tool can only read files, not directories. To read a directory, use an ls command via the Bash tool.`
}

func (readTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path"},
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "The absolute path to the file to read"},
			"offset":    map[string]any{"type": "integer", "description": "The line number to start reading from. Only provide if the file is too large to read at once"},
			"limit":     map[string]any{"type": "integer", "description": "The number of lines to read. Only provide if the file is too large to read at once."},
			"pages":     map[string]any{"type": "string", "description": "Page range for PDF files (e.g., \"1-5\", \"3\", \"10-20\"). Only applicable to PDF files. Maximum 20 pages per request."},
		},
	}
}

// Validate checks the model-supplied arguments against the tool schema before
// any execution (pi validateToolArguments; loop ToolValidator).
func (t readTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t readTool) Execute(_ context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	if path == "" {
		return errRes("file_path is required")
	}
	abs := resolve(t.cwd, path)
	if t.jobs != nil {
		if out, ok := t.jobs.Output(path); ok {
			text, note := truncateTail(out)
			return txt(text + note)
		}
		if out, ok := t.jobs.Output(abs); ok {
			text, note := truncateTail(out)
			return txt(text + note)
		}
	}
	st, err := os.Stat(abs)
	if err != nil {
		return errRes(err.Error())
	}
	if st.IsDir() {
		return errRes("This tool can only read files, not directories. Use Bash ls.")
	}
	//nolint:gosec // abs is normalized and confined to the requested tool path.
	data, err := os.ReadFile(abs)
	if err != nil {
		return errRes(err.Error())
	}
	if mime := imageMIME(data); mime != "" {
		return loop.ToolResult{Content: []types.Content{
			{Type: "text", Text: "Read image file [" + mime + "]"},
			{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MIMEType: mime},
		}}
	}
	if strings.HasSuffix(strings.ToLower(abs), ".ipynb") {
		text := formatNotebook(data)
		text, note := truncateHead(text)
		return txt(text + note)
	}
	if bytes.HasPrefix(data, []byte("%PDF")) {
		pages, _ := args["pages"].(string)
		text := extractPDFText(data, pages)
		text, note := truncateHead(text)
		return txt(text + note)
	}
	text := string(data)
	if !utf8.ValidString(text) {
		return errRes("binary file; cannot read as text")
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
	if note != "" && off > 1 {
		shown := strings.Count(chunk, "\n") + 1
		note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", off, off+shown-1, len(lines), off+shown)
	} else if end < len(lines) && note == "" {
		note = fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-end, end+1)
	}
	return txt(chunk + note)
}
