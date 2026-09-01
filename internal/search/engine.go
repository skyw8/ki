package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	globmatch "github.com/gobwas/glob"

	"ki/internal/processenv"
)

const (
	// DefaultTimeout matches Claude Code's normal ripgrep timeout. Callers can
	// still cancel earlier through the context supplied to Grep or Glob.
	DefaultTimeout = 20 * time.Second
	maxRGOutput    = 20 * 1024 * 1024
	maxRGStderr    = 1 * 1024 * 1024
	waitDelay      = 200 * time.Millisecond
)

var (
	// ErrAborted indicates that a search was canceled before completion.
	ErrAborted = errors.New("search aborted")
	// ErrTimeout indicates that a search exceeded its configured timeout.
	ErrTimeout = errors.New("search timed out")
)

// Engine runs embedded ripgrep searches.
type Engine struct {
	Timeout time.Duration
}

// GrepRequest describes a ripgrep content search.
type GrepRequest struct {
	Pattern       string
	Root          string
	Glob          []string
	Type          string
	IgnoreCase    bool
	Literal       bool
	Before        int
	After         int
	Context       int
	ShowLine      bool
	Multiline     bool
	OutputMode    string
	MaxResults    int
	NoIgnore      bool
	IncludeHidden bool
}

// Match is one ripgrep match event.
type Match struct {
	Path       string
	LineNumber int
	Text       string
}

// Count is the number of matches in one file.
type Count struct {
	Path  string
	Count int
}

// GrepResult contains raw search results. Formatting and pagination belong to
// the model-facing Grep tool, not to the process runner.
type GrepResult struct {
	Matches    []Match
	Files      []string
	Counts     []Count
	Truncated  bool
	TotalMatch int
}

// GlobRequest describes a file-name search.
type GlobRequest struct {
	Pattern       string
	Root          string
	MaxResults    int
	NoIgnore      bool
	IncludeHidden bool
	SortModified  bool
}

// GlobResult contains absolute paths returned by ripgrep.
type GlobResult struct {
	Files     []string
	Truncated bool
}

// Grep runs ripgrep in JSON mode so paths and line contents do not need to be
// parsed from the ambiguous "path:line:text" human output format.
func (e Engine) Grep(ctx context.Context, req GrepRequest) (GrepResult, error) {
	root, err := cleanExistingPath(req.Root)
	if err != nil {
		return GrepResult{}, err
	}
	if req.Pattern == "" {
		return GrepResult{}, errPatternRequired
	}
	if req.OutputMode == "files_with_matches" {
		return e.grepFiles(ctx, req, root)
	}

	args := grepArgs(req, root, true)

	var result GrepResult
	seenFiles := map[string]int{}
	lineBuffer := newLineBuffer('\n')
	stopAfter := max(req.MaxResults, 0)
	consume := func(line []byte) (bool, error) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(bytes.TrimSpace(line)) == 0 {
			return false, nil
		}
		var event rgEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return false, fmt.Errorf("parse ripgrep JSON: %w", err)
		}
		if event.Type != "match" {
			return false, nil
		}
		path, err := event.Data.Path.value()
		if err != nil {
			return false, fmt.Errorf("decode ripgrep path: %w", err)
		}
		match := Match{
			Path:       path,
			LineNumber: event.Data.LineNumber,
			Text:       strings.TrimSuffix(strings.TrimSuffix(event.Data.Lines.Text, "\n"), "\r"),
		}
		result.TotalMatch++
		if _, ok := seenFiles[path]; !ok {
			seenFiles[path] = len(result.Files)
			result.Files = append(result.Files, path)
			result.Counts = append(result.Counts, Count{Path: path})
		}
		result.Counts[seenFiles[path]].Count++
		if req.OutputMode == "files_with_matches" {
			if stopAfter > 0 && len(result.Files) >= stopAfter {
				result.Truncated = true
				return true, nil
			}
			return false, nil
		}
		if req.OutputMode == "count" {
			if stopAfter > 0 && len(result.Counts) >= stopAfter {
				result.Truncated = true
				return true, nil
			}
			return false, nil
		}
		result.Matches = append(result.Matches, match)
		if stopAfter > 0 && len(result.Matches) >= stopAfter {
			result.Truncated = true
			return true, nil
		}
		return false, nil
	}

	runResult, err := e.run(ctx, filepath.Dir(root), args, func(chunk []byte) (bool, error) {
		return lineBuffer.feed(chunk, consume)
	})
	if err != nil {
		// Keep data already parsed before a timeout. ripgrep may have produced
		// useful matches even though the traversal did not finish.
		if !errors.Is(err, ErrTimeout) || (len(result.Matches) == 0 && len(result.Files) == 0) {
			return GrepResult{}, err
		}
		if flushErr := lineBuffer.flush(consume); flushErr != nil {
			return GrepResult{}, flushErr
		}
		result.Truncated = true
	}
	result.Truncated = result.Truncated || runResult.Truncated
	if !runResult.Truncated {
		if err := lineBuffer.flush(consume); err != nil {
			return GrepResult{}, err
		}
	}
	if err := validateGrepResult(result); err != nil {
		return GrepResult{}, err
	}
	sort.Strings(result.Files)
	sort.Slice(result.Counts, func(i, j int) bool { return result.Counts[i].Path < result.Counts[j].Path })
	return result, nil
}

func (e Engine) grepFiles(ctx context.Context, req GrepRequest, root string) (GrepResult, error) {
	args := grepArgs(req, root, false)
	args = append([]string{args[0], "--files-with-matches", "--null"}, args[1:]...)
	result := GrepResult{}
	buffer := newLineBuffer(0)
	limit := req.MaxResults
	consume := func(raw []byte) (bool, error) {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if len(raw) == 0 {
			return false, nil
		}
		result.Files = append(result.Files, filepath.Clean(string(raw)))
		if limit > 0 && len(result.Files) >= limit {
			result.Truncated = true
			return true, nil
		}
		return false, nil
	}
	runResult, err := e.run(ctx, filepath.Dir(root), args, func(chunk []byte) (bool, error) {
		return buffer.feed(chunk, consume)
	})
	if err != nil {
		if !errors.Is(err, ErrTimeout) || len(result.Files) == 0 {
			return GrepResult{}, err
		}
		if flushErr := buffer.flush(consume); flushErr != nil {
			return GrepResult{}, flushErr
		}
		result.Truncated = true
	}
	result.Truncated = result.Truncated || runResult.Truncated
	if !runResult.Truncated {
		if err := buffer.flush(consume); err != nil {
			return GrepResult{}, err
		}
	}
	sort.Strings(result.Files)
	return result, nil
}

func grepArgs(req GrepRequest, root string, jsonOutput bool) []string {
	args := []string{"--color=never", "--hidden", "--max-columns", "500"}
	if jsonOutput {
		args = append([]string{"--json"}, args...)
	}
	if !req.IncludeHidden {
		args = removeArg(args, "--hidden")
	}
	if req.NoIgnore {
		args = append(args, "--no-ignore")
	}
	for _, dir := range []string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl"} {
		args = append(args, "--glob", "!"+dir)
	}
	if req.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if req.IgnoreCase {
		args = append(args, "-i")
	}
	if req.Literal {
		args = append(args, "--fixed-strings")
	}
	if req.Type != "" {
		args = append(args, "--type", req.Type)
	}
	for _, pattern := range req.Glob {
		if pattern != "" {
			args = append(args, "--glob", pattern)
		}
	}
	if jsonOutput && req.OutputMode == "content" && req.ShowLine {
		args = append(args, "--line-number")
	}
	if req.Pattern[0] == '-' {
		args = append(args, "-e", req.Pattern)
	} else {
		args = append(args, req.Pattern)
	}
	return append(args, "--", root)
}

// Glob lists files with ripgrep's --files traversal. NUL delimiters keep paths
// containing newlines unambiguous.
func (e Engine) Glob(ctx context.Context, req GlobRequest) (GlobResult, error) {
	root, err := cleanExistingPath(req.Root)
	if err != nil {
		return GlobResult{}, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return GlobResult{}, fmt.Errorf("%w: %s", errPathNotExist, root)
	}
	if !st.IsDir() {
		return GlobResult{}, fmt.Errorf("%w: %s", errPathNotDirectory, root)
	}
	if req.Pattern == "" {
		return GlobResult{}, errPatternRequired
	}

	args := []string{"--files", "--null"}
	var matcher globmatch.Glob
	if req.NoIgnore {
		args = append(args, "--glob", req.Pattern)
	} else {
		// An explicit ripgrep --glob whitelist overrides ignore files. Filter
		// the already ignored file list instead when respect-ignore is enabled.
		matcher, err = globmatch.Compile(filepath.ToSlash(req.Pattern), '/')
		if err != nil {
			return GlobResult{}, fmt.Errorf("invalid glob pattern: %w", err)
		}
	}
	if req.SortModified {
		args = append(args, "--sort=modified")
	}
	if req.NoIgnore {
		args = append(args, "--no-ignore")
	}
	if req.IncludeHidden {
		args = append(args, "--hidden")
	}
	// Run from root and search "." so ripgrep discovers repository ignore
	// files. Passing the same directory as an explicit absolute operand causes
	// ripgrep to bypass those rules even when --no-ignore is absent.
	args = append(args, "--", ".")

	limit := max(req.MaxResults, 0)
	result := GlobResult{}
	buffer := newLineBuffer(0)
	consume := func(raw []byte) (bool, error) {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if len(raw) == 0 {
			return false, nil
		}
		path := string(raw)
		if matcher != nil {
			rel := strings.TrimPrefix(filepath.ToSlash(path), "./")
			if !matcher.Match(rel) {
				return false, nil
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(path))
		}
		result.Files = append(result.Files, filepath.Clean(path))
		if limit > 0 && len(result.Files) >= limit {
			result.Truncated = true
			return true, nil
		}
		return false, nil
	}
	runResult, err := e.run(ctx, root, args, func(chunk []byte) (bool, error) {
		return buffer.feed(chunk, consume)
	})
	if err != nil {
		if !errors.Is(err, ErrTimeout) || len(result.Files) == 0 {
			return GlobResult{}, err
		}
		if flushErr := buffer.flush(consume); flushErr != nil {
			return GlobResult{}, flushErr
		}
		result.Truncated = true
	}
	result.Truncated = result.Truncated || runResult.Truncated
	if !runResult.Truncated {
		if err := buffer.flush(consume); err != nil {
			return GlobResult{}, err
		}
	}
	return result, nil
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path  rgPath `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

type rgPath struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

func (p rgPath) value() (string, error) {
	if p.Text != "" {
		return p.Text, nil
	}
	if p.Bytes == "" {
		return "", errEmptyRGPath
	}
	b, err := base64.StdEncoding.DecodeString(p.Bytes)
	return string(b), err
}

type lineBuffer struct {
	sep     byte
	pending []byte
}

func newLineBuffer(sep byte) *lineBuffer { return &lineBuffer{sep: sep} }

func (b *lineBuffer) feed(chunk []byte, consume func([]byte) (bool, error)) (bool, error) {
	b.pending = append(b.pending, chunk...)
	for {
		i := bytes.IndexByte(b.pending, b.sep)
		if i < 0 {
			return false, nil
		}
		part := append([]byte(nil), b.pending[:i]...)
		b.pending = b.pending[i+1:]
		stop, err := consume(part)
		if err != nil || stop {
			return stop, err
		}
	}
}

func (b *lineBuffer) flush(consume func([]byte) (bool, error)) error {
	if len(b.pending) == 0 {
		return nil
	}
	_, err := consume(b.pending)
	b.pending = nil
	return err
}

type runResult struct {
	Truncated bool
}

type chunkConsumer func([]byte) (stop bool, err error)

func (e Engine) run(ctx context.Context, dir string, args []string, consume chunkConsumer) (runResult, error) {
	result, err := e.runOnce(ctx, dir, args, consume)
	message := ""
	if err != nil {
		message = strings.ToLower(err.Error())
	}
	if err == nil || ctx.Err() != nil || (!strings.Contains(message, "eagain") && !strings.Contains(message, "resource temporarily unavailable")) {
		return result, err
	}
	// Large repositories can make ripgrep's parallel workers hit EAGAIN. A
	// serial retry is preferable to turning a valid search into a hard error.
	retryArgs := append([]string{"-j", "1"}, args...)
	return e.runOnce(ctx, dir, retryArgs, consume)
}

func (e Engine) runOnce(ctx context.Context, dir string, args []string, consume chunkConsumer) (runResult, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rgPath, cleanup, err := executable()
	if err != nil {
		return runResult{}, err
	}
	defer cleanup()

	cmd := exec.CommandContext(runCtx, rgPath, args...) //nolint:gosec // rgPath is Ki's embedded/system search executable
	cmd.Dir = dir
	cmd.Env = processenv.WithProxyEnvironment(processenv.ChildEnvironment())
	detachRGCommand(cmd)
	cmd.Cancel = func() error {
		killRGCommand(cmd)
		return nil
	}
	cmd.WaitDelay = waitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runResult{}, fmt.Errorf("capture ripgrep stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return runResult{}, fmt.Errorf("capture ripgrep stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return runResult{}, fmt.Errorf("start ripgrep: %w", err)
	}

	stderrDone := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.CopyN(&b, stderr, maxRGStderr)
		// Drain the remainder so a noisy child cannot block on a full stderr
		// pipe after the diagnostic cap has been reached.
		_, _ = io.Copy(io.Discard, stderr)
		stderrDone <- strings.TrimSpace(b.String())
	}()

	var outputBytes int
	var stop bool
	var consumeErr error
	buf := make([]byte, 64*1024)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			outputBytes += n
			if outputBytes > maxRGOutput {
				stop = true
				killRGCommand(cmd)
				break
			}
			var requestedStop bool
			requestedStop, consumeErr = consume(buf[:n])
			if consumeErr != nil {
				killRGCommand(cmd)
				break
			}
			if requestedStop {
				stop = true
				killRGCommand(cmd)
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !stop && consumeErr == nil {
				consumeErr = fmt.Errorf("read ripgrep stdout: %w", readErr)
			}
			break
		}
	}
	waitErr := cmd.Wait()
	stderrText := <-stderrDone

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return runResult{}, fmt.Errorf("%w after %s", ErrTimeout, timeout.Round(time.Second))
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.Canceled) {
		return runResult{}, ErrAborted
	}
	if consumeErr != nil {
		return runResult{}, consumeErr
	}
	if stop {
		return runResult{Truncated: true}, nil
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
			return runResult{}, nil
		}
		if stderrText != "" {
			return runResult{}, fmt.Errorf("%w: %s", errRipgrep, stderrText)
		}
		return runResult{}, fmt.Errorf("ripgrep: %w", waitErr)
	}
	return runResult{}, nil
}

func cleanExistingPath(path string) (string, error) {
	if path == "" {
		path, _ = os.Getwd()
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve search path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("%w: %s", errPathNotExist, path)
	}
	return filepath.Clean(abs), nil
}

func removeArg(args []string, target string) []string {
	out := args[:0]
	for _, arg := range args {
		if arg != target {
			out = append(out, arg)
		}
	}
	return out
}

func validateGrepResult(result GrepResult) error {
	for _, match := range result.Matches {
		if match.Path == "" || match.LineNumber < 1 {
			return errInvalidRGMatch
		}
	}
	return nil
}
