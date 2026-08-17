package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ki/internal/loop"
	"ki/internal/types"
)

const (
	maxLines = 2000
	maxBytes = 50 * 1024
)

// Set is the four built-in tools bound to a session cwd.
type Set struct {
	CWD  string
	Jobs *JobStore
}

// All returns the four tools.
func (s Set) All() []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	cwd := s.CWD
	jobs := s.Jobs
	return []loop.Tool{
		readTool{cwd: cwd, jobs: jobs},
		writeTool{cwd: cwd},
		editTool{cwd: cwd},
		bashTool{cwd: cwd, jobs: jobs},
	}
}

func resolve(cwd, p string) string {
	if p == "" {
		return cwd
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if cwd == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(cwd, p))
}

func truncateHead(s string) (out string, note string) {
	lines := strings.Split(s, "\n")
	total := len(lines)
	n := total
	if n > maxLines {
		n = maxLines
	}
	chunk := strings.Join(lines[:n], "\n")
	for len(chunk) > maxBytes && n > 1 {
		n--
		chunk = strings.Join(lines[:n], "\n")
	}
	if n < total {
		return chunk, fmt.Sprintf("\n\n[Showing lines 1-%d of %d. Use offset=%d to continue.]", n, total, n+1)
	}
	if len(chunk) > maxBytes {
		return chunk[:maxBytes], fmt.Sprintf("\n\n[%d byte limit reached. Use offset to continue.]", maxBytes)
	}
	return chunk, ""
}

func truncateTail(s string) (out, note string) {
	if len(s) <= maxBytes {
		lines := strings.Split(s, "\n")
		if len(lines) <= maxLines {
			return s, ""
		}
	}
	lines := strings.Split(s, "\n")
	total := len(lines)
	start := 0
	if total > maxLines {
		start = total - maxLines
	}
	chunk := strings.Join(lines[start:], "\n")
	for len(chunk) > maxBytes && start < total-1 {
		start++
		chunk = strings.Join(lines[start:], "\n")
	}
	return chunk, fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%d byte limit).]", start+1, total, total, maxBytes)
}

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

func (t readTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
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
	start := off - 1
	if start < 0 {
		start = 0
	}
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

func formatNotebook(data []byte) string {
	var nb struct {
		Cells []struct {
			CellType string `json:"cell_type"`
			Source   any    `json:"source"`
			Outputs  []struct {
				Text any `json:"text"`
			} `json:"outputs"`
		} `json:"cells"`
	}
	if json.Unmarshal(data, &nb) != nil {
		return string(data)
	}
	var b strings.Builder
	for i, c := range nb.Cells {
		fmt.Fprintf(&b, "=== cell %d (%s) ===\n", i, c.CellType)
		b.WriteString(joinSrc(c.Source))
		b.WriteByte('\n')
		for _, o := range c.Outputs {
			if s := joinSrc(o.Text); s != "" {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func joinSrc(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var s string
		for _, x := range t {
			s += fmt.Sprint(x)
		}
		return s
	}
	return ""
}

func extractPDFText(data []byte, pages string) string {
	// Best-effort: pull printable strings from PDF streams.
	var b strings.Builder
	if pages != "" {
		fmt.Fprintf(&b, "[pages=%s]\n", pages)
	}
	inParen := false
	var cur []byte
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '(' {
			inParen = true
			cur = cur[:0]
			continue
		}
		if inParen && c == ')' {
			inParen = false
			if len(cur) > 0 && utf8.Valid(cur) {
				b.Write(cur)
				b.WriteByte('\n')
			}
			continue
		}
		if inParen {
			cur = append(cur, c)
		}
	}
	if b.Len() == 0 {
		return "[PDF: no extractable text]"
	}
	return b.String()
}

func imageMIME(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) > 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

type writeTool struct{ cwd string }

func (writeTool) Name() string        { return "Write" }
func (writeTool) Description() string { return "Write a file to the local filesystem." }
func (writeTool) Snippet() string     { return "Create or overwrite files" }
func (writeTool) Prompt() string {
	return `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- Prefer the Edit tool for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`
}
func (writeTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path", "content"},
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "The absolute path to the file to write (must be absolute, not relative)"},
			"content":   map[string]any{"type": "string", "description": "The content to write to the file"},
		},
	}
}

func (t writeTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t writeTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return errRes("file_path is required")
	}
	abs := resolve(t.cwd, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return errRes(err.Error())
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return errRes(err.Error())
	}
	return okRes(fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path))
}

type editTool struct{ cwd string }

func (editTool) Name() string        { return "Edit" }
func (editTool) Description() string { return "A tool for editing files" }
func (editTool) Snippet() string {
	return "Make precise file edits with exact text replacement"
}
func (editTool) Prompt() string {
	return `Performs exact string replacements in files.

Usage:
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it. Avoid adding emojis to files unless asked.
- The edit will FAIL if old_string is not unique in the file. Either provide a larger string with more surrounding context to make it unique or use replace_all to change every instance of old_string.
- Use replace_all for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`
}
func (editTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path", "old_string", "new_string"},
		"properties": map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to modify"},
			"old_string":  map[string]any{"type": "string", "description": "The text to replace"},
			"new_string":  map[string]any{"type": "string", "description": "The text to replace it with (must be different from old_string)"},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences of old_string (default false)"},
		},
	}
}

func (t editTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t editTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	oldS, _ := args["old_string"].(string)
	newS, _ := args["new_string"].(string)
	if path == "" || oldS == "" {
		return errRes("file_path and old_string are required")
	}
	if oldS == newS {
		return errRes("No changes to make: old_string and new_string are exactly the same.")
	}
	abs := resolve(t.cwd, path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return errRes(fmt.Sprintf("Could not edit file: %s. %s", path, err.Error()))
	}
	text := string(data)
	count := strings.Count(text, oldS)
	if count == 0 {
		return errRes(fmt.Sprintf("Could not find old_string in %s. The old_string must match exactly including all whitespace and newlines.", path))
	}
	all := false
	switch v := args["replace_all"].(type) {
	case bool:
		all = v
	case string:
		all = v == "true"
	}
	if count > 1 && !all {
		return errRes(fmt.Sprintf("Found %d occurrences of old_string in %s. Each old_string must be unique. Use replace_all or provide more context.", count, path))
	}
	var next string
	n := 1
	if all {
		n = -1
	}
	next = strings.Replace(text, oldS, newS, n)
	if err := os.WriteFile(abs, []byte(next), 0o644); err != nil {
		return errRes(err.Error())
	}
	repl := 1
	if all {
		repl = count
	}
	return okRes(fmt.Sprintf("Successfully replaced %d block(s) in %s.", repl, path))
}

type bashTool struct {
	cwd  string
	jobs *JobStore
}

func (bashTool) Name() string { return "Bash" }
func (bashTool) Description() string {
	return "Run shell command"
}
func (bashTool) Snippet() string { return "Execute bash commands (ls, grep, find, etc.)" }
func (bashTool) Prompt() string {
	return `Executes a given bash command and returns its output.

The working directory persists between commands, but shell state does not. The shell environment is initialized from the user's profile (bash or zsh).

IMPORTANT: Avoid using this tool to run cat, head, tail, sed, awk, or echo commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool.

You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). By default, your command will timeout after 120000ms (2 minutes).

You can use the run_in_background parameter to run the command in the background. Only use this if you don't need the result immediately.

When issuing multiple commands:
- If the commands are independent and can run in parallel, make multiple Bash tool calls in a single message.
- If the commands depend on each other and must run sequentially, use a single Bash call with '&&' to chain them together.
- DO NOT use newlines to separate commands (newlines are ok in quoted strings).`
}
func (bashTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"command"},
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": "The command to execute"},
			"timeout":           map[string]any{"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
			"description":       map[string]any{"type": "string", "description": "Clear, concise description of what this command does in active voice."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Set to true to run this command in the background. Use Read to read the output later."},
		},
	}
}

func (t bashTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t bashTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		return errRes("command is required")
	}
	timeout := 120000
	if v, ok := asInt(args["timeout"]); ok && v > 0 {
		timeout = v
		if timeout > 600000 {
			timeout = 600000
		}
	}
	bg := false
	if v, ok := args["run_in_background"].(bool); ok {
		bg = v
	}
	if bg {
		id, path := t.jobs.Start(t.cwd, cmdStr)
		return okRes(fmt.Sprintf("started background command %s\noutput_file: %s\nUse Read with file_path=%s to check output.", id, path, path))
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	out, code, err := runBash(cctx, t.cwd, cmdStr)
	text, note := truncateTail(out)
	text += note
	if cctx.Err() == context.DeadlineExceeded {
		return errRes(appendStatus(text, fmt.Sprintf("Command timed out after %d seconds", timeout/1000)))
	}
	if err != nil && code == 0 {
		return errRes(appendStatus(text, err.Error()))
	}
	if code != 0 {
		return errRes(appendStatus(text, fmt.Sprintf("Command exited with code %d", code)))
	}
	if text == "" {
		text = "(no output)"
	}
	return okRes(text)
}

func runBash(ctx context.Context, cwd, command string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		}
	}
	return buf.String(), code, err
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

// JobStore holds background bash jobs.
type JobStore struct {
	mu   sync.Mutex
	jobs map[string]*bgJob
}

type bgJob struct {
	path string
	buf  bytes.Buffer
}

// NewJobStore creates an empty store.
func NewJobStore() *JobStore { return &JobStore{jobs: map[string]*bgJob{}} }

// Start launches command in the background and returns id + output path.
func (s *JobStore) Start(cwd, command string) (id, path string) {
	id = fmt.Sprintf("bg-%d", time.Now().UnixNano())
	f, err := os.CreateTemp("", "ki-bg-*.log")
	if err != nil {
		return id, ""
	}
	path = f.Name()
	job := &bgJob{path: path}
	s.mu.Lock()
	s.jobs[id] = job
	s.jobs[path] = job
	s.mu.Unlock()
	go func() {
		cmd := exec.Command("bash", "-lc", command)
		if cwd != "" {
			cmd.Dir = cwd
		}
		cmd.Stdout = io.MultiWriter(f, &job.buf)
		cmd.Stderr = cmd.Stdout
		_ = cmd.Run()
		_ = f.Close()
	}()
	return id, path
}

// Output returns captured output for a job id or path.
func (s *JobStore) Output(key string) (string, bool) {
	s.mu.Lock()
	j, ok := s.jobs[key]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	b, _ := os.ReadFile(j.path)
	if len(b) > 0 {
		return string(b), true
	}
	return j.buf.String(), true
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func errRes(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}, IsError: true}
}

// validateArgs runs the tool schema against model arguments (loop ToolValidator).
func validateArgs(schema map[string]any, name string, args map[string]any) error {
	if msg := loop.SchemaErrors(schema, name, args); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return nil
}
func okRes(s string) loop.ToolResult {
	return txt(s)
}
func txt(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}}
}
