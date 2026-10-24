package filetools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
	"ki/internal/types"
)

type readTool struct {
	cwd  string
	rich bool
	ops  ReadOperations
}

type ReadOperations interface {
	Stat(context.Context, string) (os.FileInfo, error)
	ReadFile(context.Context, string) ([]byte, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type localReadOperations struct{}

func (localReadOperations) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(path)
}

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
	b, err := os.ReadFile(path) //nolint:gosec // path was normalized and validated by the read tool
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
	NextOffset     int    `json:"next_offset,omitzero"`
	NextByteOffset *int64 `json:"next_byte_offset,omitempty"`
}

type readDetails struct {
	Truncation *truncationDetails `json:"truncation,omitempty"`
	Image      *imageDetails      `json:"image,omitempty"`
}

func (readTool) Name() string { return catalog.Read }

func (readTool) Description() string { return "Read a file from the local filesystem." }

func (readTool) Snippet() string { return "Read file contents" }

func (t readTool) Prompt() string {
	base := `Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters
- If a single line exceeds the page byte budget, continue with the returned next_byte_offset as byte_offset; this is a zero-based UTF-8 boundary and cannot be combined with offset`
	if t.rich {
		base += `
- This tool allows reading images (eg PNG, JPG, etc).
- This tool can read PDF files (.pdf). For large PDFs (more than 10 pages), you MUST provide the pages parameter to read specific page ranges (e.g., pages: "1-5"). Maximum 20 pages per request.`
	}
	return base + `
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs, combining code, text, and visualizations.
- This tool can only read files, not directories. To list a directory, use the Glob tool.`
}

func (t readTool) Parameters() map[string]any {
	properties := map[string]any{
		"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to read"},
		"offset":      map[string]any{"type": "integer", "description": "The line number to start reading from. Only provide if the file is too large to read at once"},
		"byte_offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based raw byte position for continuing a byte-truncated line. Must be a UTF-8 boundary; do not combine with offset."},
		"limit":       map[string]any{"type": "integer", "description": "The number of lines to read. Only provide if the file is too large to read at once."},
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

func (t readTool) Validate(args map[string]any) error {
	return support.ValidateArgs(t.Parameters(), t.Name(), args)
}

func (t readTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	path, _ := args["file_path"].(string)
	if path == "" {
		return support.Error("file_path is required")
	}
	abs := resolve(t.cwd, path)
	ops := t.ops
	if ops == nil {
		ops = localReadOperations{}
	}
	st, err := ops.Stat(ctx, abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return support.DiagnosticError(err.Error(), "rejected", "not_found", "workspace_state")
		}
		if errors.Is(err, context.Canceled) {
			return support.DiagnosticError("Read aborted", "cancelled", "cancelled_by_parent", "cancellation")
		}
		return support.Error(err.Error())
	}
	if st.IsDir() {
		return support.Error("This tool can only read files, not directories. Use Glob.")
	}

	stream, err := ops.Open(ctx, abs)
	if err != nil {
		return readFailure(err)
	}
	defer stream.Close()
	prefix := make([]byte, 16)
	n, err := io.ReadFull(stream, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return readFailure(err)
	}
	prefix = prefix[:n]
	if imageMIME(prefix) == "" && !bytes.HasPrefix(prefix, []byte("%PDF")) && !strings.HasSuffix(strings.ToLower(abs), ".ipynb") {
		return readTextStream(ctx, io.MultiReader(bytes.NewReader(prefix), stream), args)
	}
	data, err := ops.ReadFile(ctx, abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return support.DiagnosticError(err.Error(), "rejected", "not_found", "workspace_state")
		}
		if errors.Is(err, context.Canceled) {
			return support.DiagnosticError("Read aborted", "cancelled", "cancelled_by_parent", "cancellation")
		}
		return support.Error(err.Error())
	}
	if mime := imageMIME(data); mime != "" {
		if !t.rich {
			return support.Error("image files are unavailable for the selected text-only model")
		}
		processed, outMIME, details, err := resizeImageForModel(data, mime)
		if err != nil {
			return support.Error("Could not process image: " + err.Error())
		}
		if err := ctx.Err(); err != nil {
			return support.Error("Read aborted")
		}
		return toolapi.Result{Content: []types.Content{
			{Type: "text", Text: "Read image file [" + outMIME + "]"},
			{Type: "image", Data: base64.StdEncoding.EncodeToString(processed), MIMEType: outMIME},
		}, Details: readDetails{Image: details}}
	}
	if strings.HasSuffix(strings.ToLower(abs), ".ipynb") {
		text := formatNotebook(data)
		text, note := truncateHead(text)
		return support.Text(text + note)
	}
	if bytes.HasPrefix(data, []byte("%PDF")) {
		if !t.rich {
			return support.Error("PDF files are unavailable for the selected text-only model")
		}
		pages, _ := args["pages"].(string)
		text := extractPDFText(data, pages)
		text, note := truncateHead(text)
		return support.Text(text + note)
	}
	return support.Error("unsupported file format")
}
