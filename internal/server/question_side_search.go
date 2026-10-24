package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	globmatch "github.com/gobwas/glob"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

const (
	sideSearchMaxFiles     = 10000
	sideSearchMaxFileBytes = 2 << 20
	sideSearchMaxBytes     = 32 << 20
	sideSearchMaxOutput    = 48 << 10
	sideSearchMaxHits      = 1000
)

type sideSearchFile struct {
	path     string
	rel      string
	modified time.Time
}

type sideIgnoreRule struct {
	base      string
	pattern   string
	negate    bool
	directory bool
}

type sideSearchWalk struct {
	ctx           context.Context
	root          *os.Root
	base          string
	files         []sideSearchFile
	visited       int
	truncated     bool
	respectIgnore bool
}

func openPinnedSideRoot(path string) (*os.Root, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, errors.New("side search roots must be absolute")
	}
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		before, err := root.Lstat(part)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, errors.New("side search root contains a non-directory or symbolic link")
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		after, err := next.Stat(".")
		_ = root.Close()
		if err != nil || !os.SameFile(before, after) {
			_ = next.Close()
			return nil, errors.New("side search root changed during admission")
		}
		root = next
	}
	return root, nil
}

func openSideSearchPath(path string, roots []string) (*os.Root, string, string, error) {
	for _, allowed := range roots {
		rel, err := filepath.Rel(allowed, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		root, err := openPinnedSideRoot(allowed)
		if err != nil {
			return nil, "", "", err
		}
		if _, err := root.Stat(rel); err != nil {
			_ = root.Close()
			return nil, "", "", err
		}
		return root, rel, allowed, nil
	}
	return nil, "", "", errors.New("outside permitted side conversation roots")
}

func sideGlobMatch(pattern, path string, basename bool) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	matcher, err := globmatch.Compile(pattern, '/')
	if err != nil {
		return false, err
	}
	if matcher.Match(path) || (basename && !strings.Contains(pattern, "/") && matcher.Match(filepath.Base(path))) {
		return true, nil
	}
	if strings.HasPrefix(pattern, "**/") {
		return sideGlobMatch(strings.TrimPrefix(pattern, "**/"), path, basename)
	}
	return false, nil
}

func sideIgnored(path string, directory bool, rules []sideIgnoreRule) (bool, error) {
	ignored := false
	for _, rule := range rules {
		relative := path
		if rule.base != "." {
			prefix := filepath.ToSlash(rule.base) + "/"
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			relative = strings.TrimPrefix(path, prefix)
		}
		if rule.directory && !directory {
			continue
		}
		anchored := strings.HasPrefix(rule.pattern, "/")
		match, err := sideGlobMatch(strings.TrimPrefix(rule.pattern, "/"), relative, !anchored)
		if err != nil {
			return false, fmt.Errorf("invalid workspace ignore pattern: %w", err)
		}
		if match {
			ignored = !rule.negate
		}
	}
	return ignored, nil
}

func (walk *sideSearchWalk) ignoreRules(dir string, inherited []sideIgnoreRule) ([]sideIgnoreRule, error) {
	rules := slices.Clone(inherited)
	if !walk.respectIgnore {
		return rules, nil
	}
	for _, name := range []string{".gitignore", ".ignore", ".rgignore"} {
		file, err := walk.root.Open(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			_ = file.Close()
			return nil, errors.New("invalid or oversized workspace ignore file")
		}
		data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		_ = file.Close()
		if err != nil || len(data) > 64<<10 {
			return nil, errors.New("cannot read workspace ignore file")
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rule := sideIgnoreRule{base: dir}
			rule.negate = strings.HasPrefix(line, "!")
			if rule.negate {
				line = strings.TrimPrefix(line, "!")
			}
			rule.directory = strings.HasSuffix(line, "/")
			rule.pattern = strings.TrimSuffix(line, "/")
			if rule.pattern != "" {
				rules = append(rules, rule)
			}
		}
	}
	return rules, nil
}

func (walk *sideSearchWalk) visit(dir string, inherited []sideIgnoreRule) error {
	if err := walk.ctx.Err(); err != nil {
		return err
	}
	rules, err := walk.ignoreRules(dir, inherited)
	if err != nil {
		return err
	}
	file, err := walk.root.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	for {
		entries, err := file.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			if err := walk.ctx.Err(); err != nil {
				return err
			}
			walk.visited++
			if walk.visited > sideSearchMaxFiles {
				walk.truncated = true
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			relative := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", ".svn", ".hg", ".bzr", ".jj", ".sl":
					continue
				}
			}
			ignored, matchErr := sideIgnored(filepath.ToSlash(relative), entry.IsDir(), rules)
			if matchErr != nil {
				return matchErr
			}
			if ignored {
				continue
			}
			if entry.IsDir() {
				if err := walk.visit(relative, rules); err != nil {
					return err
				}
				if walk.truncated {
					return nil
				}
				continue
			}
			info, statErr := walk.root.Stat(relative)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			walk.files = append(walk.files, sideSearchFile{
				path: filepath.Join(walk.base, relative), rel: relative, modified: info.ModTime(),
			})
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func sideSearchInt(args map[string]any, key string, fallback int) (int, error) {
	if args[key] == nil {
		return fallback, nil
	}
	var number int
	switch value := args[key].(type) {
	case int:
		number = value
	case float64:
		if value < 0 || value > 1<<30 || float64(int(value)) != value {
			return 0, fmt.Errorf("%s must be a non-negative integer", key)
		}
		number = int(value)
	default:
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	if number < 0 || number > 1<<30 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return number, nil
}

func sideTypePattern(name string) (string, bool) {
	patterns := map[string]string{
		"go": "*.go", "js": "*.{js,jsx,mjs,cjs}", "ts": "*.{ts,tsx,mts,cts}",
		"py": "*.{py,pyi}", "rust": "*.rs", "json": "*.json", "jsonl": "*.jsonl",
		"md": "*.{md,markdown}", "yaml": "*.{yaml,yml}", "toml": "*.toml",
		"html": "*.{html,htm}", "css": "*.{css,scss,sass,less}", "xml": "*.xml",
		"java": "*.java", "c": "*.{c,h}", "cpp": "*.{cc,cpp,cxx,h,hpp,hxx}",
		"sh": "*.{sh,bash,zsh}", "ruby": "*.rb", "rb": "*.rb", "php": "*.php",
		"sql": "*.sql", "txt": "*.txt", "text": "*.txt", "vue": "*.vue",
		"svelte": "*.svelte", "swift": "*.swift", "kotlin": "*.{kt,kts}",
		"cs": "*.cs", "proto": "*.proto", "docker": "*Dockerfile*", "make": "*{Makefile,makefile}*",
	}
	pattern, ok := patterns[name]
	return pattern, ok
}

type sideSearchHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type sideSearchCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

func executeQuestionSideSearch(ctx context.Context, name string, args map[string]any, cwd string, roots []string) toolapi.Result {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path, _ := args["path"].(string)
	if path == "" {
		path = cwd
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	root, relative, base, err := openSideSearchPath(filepath.Clean(path), roots)
	if err != nil {
		return questionSideError(err.Error())
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(relative)
	if err != nil {
		return questionSideError(err.Error())
	}
	respect := true
	if value, ok := args["respect_gitignore"].(bool); ok {
		respect = value
	}
	walk := sideSearchWalk{ctx: ctx, root: root, base: base, respectIgnore: respect}
	if info.IsDir() {
		var inherited []sideIgnoreRule
		if respect && relative != "." {
			parent := "."
			for _, part := range strings.Split(relative, string(filepath.Separator)) {
				inherited, err = walk.ignoreRules(parent, inherited)
				if err != nil {
					return questionSideError(err.Error())
				}
				if part != "." {
					parent = filepath.Join(parent, part)
				}
			}
		}
		err = walk.visit(relative, inherited)
	} else if name == catalog.Glob {
		return questionSideError("glob path must be a directory")
	} else if info.Mode().IsRegular() {
		walk.files = []sideSearchFile{{path: path, rel: relative, modified: info.ModTime()}}
	} else {
		return questionSideError("search path must be a regular file or directory")
	}
	if err != nil {
		return questionSideError(fmt.Sprintf("confined search failed: %v", err))
	}
	if name == catalog.Glob {
		return executeSideGlob(args, walk, cwd, path)
	}
	return executeSideGrep(ctx, args, walk, cwd, path)
}

func executeSideGlob(args map[string]any, walk sideSearchWalk, cwd, requested string) toolapi.Result {
	pattern, _ := args["pattern"].(string)
	if pattern == "" || len(pattern) > 64<<10 || filepath.IsAbs(pattern) || filepath.VolumeName(pattern) != "" {
		return questionSideError("glob requires a non-empty relative pattern")
	}
	for _, part := range strings.Split(filepath.ToSlash(pattern), "/") {
		if part == ".." {
			return questionSideError("glob cannot traverse parent directories")
		}
	}
	if _, err := sideGlobMatch(pattern, "", false); err != nil {
		return questionSideError(err.Error())
	}
	slices.SortFunc(walk.files, func(a, b sideSearchFile) int {
		if comparison := b.modified.Compare(a.modified); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.path, b.path)
	})
	var paths []string
	bytes := 0
	for _, file := range walk.files {
		rel, _ := filepath.Rel(requested, file.path)
		matched, _ := sideGlobMatch(pattern, filepath.ToSlash(rel), false)
		if !matched {
			continue
		}
		if len(paths) >= sideSearchMaxHits || bytes+len(file.path) > sideSearchMaxOutput {
			walk.truncated = true
			break
		}
		paths = append(paths, file.path)
		bytes += len(file.path) + 1
	}
	text := strings.Join(paths, "\n")
	if text == "" {
		text = "No files found"
	}
	if walk.truncated {
		text += "\n[Results truncated by the confined search budget.]"
	}
	return toolapi.Result{
		Content: []types.Content{{Type: "text", Text: text}},
		Details: map[string]any{"paths": paths, "root": requested, "files": len(paths), "truncated": walk.truncated, "engine": "confined"},
	}
}

func executeSideGrep(ctx context.Context, args map[string]any, walk sideSearchWalk, cwd, requested string) toolapi.Result {
	pattern, _ := args["pattern"].(string)
	if pattern == "" || len(pattern) > 64<<10 {
		return questionSideError("pattern must be non-empty and at most 64 KiB")
	}
	flags := "(?m)"
	if value, _ := args["-i"].(bool); value {
		flags = "(?mi)"
	}
	multiline, _ := args["multiline"].(bool)
	if multiline {
		flags = strings.TrimSuffix(flags, ")") + "s)"
	}
	matcher, err := regexp.Compile(flags + pattern)
	if err != nil {
		return questionSideError("confined search uses RE2 syntax: " + err.Error())
	}
	mode, _ := args["output_mode"].(string)
	if mode == "" {
		mode = "files_with_matches"
	}
	if mode != "files_with_matches" && mode != "content" && mode != "count" {
		return questionSideError("output_mode must be content, files_with_matches, or count")
	}
	limit, err := sideSearchInt(args, "head_limit", 250)
	if err != nil {
		return questionSideError(err.Error())
	}
	if limit == 0 || limit > sideSearchMaxHits {
		limit = sideSearchMaxHits
	}
	offset, err := sideSearchInt(args, "offset", 0)
	if err != nil {
		return questionSideError(err.Error())
	}
	before, after := 0, 0
	for _, key := range []string{"context", "-C", "-B", "-A"} {
		value, err := sideSearchInt(args, key, 0)
		if err != nil {
			return questionSideError(err.Error())
		}
		value = min(value, 20)
		if key == "context" || key == "-C" {
			if value > 0 {
				before, after = value, value
				break
			}
		} else if key == "-B" {
			before = value
		} else {
			after = value
		}
	}
	showLine := true
	if value, ok := args["-n"].(bool); ok {
		showLine = value
	}
	filter, _ := args["glob"].(string)
	if len(filter) > 16<<10 {
		return questionSideError("glob filter exceeds 16 KiB")
	}
	typeName, _ := args["type"].(string)
	typePattern := ""
	if typeName != "" {
		var ok bool
		typePattern, ok = sideTypePattern(typeName)
		if !ok {
			return questionSideError("unsupported confined search type; use glob to filter this file type")
		}
	}
	if filter != "" {
		if _, err := sideGlobMatch(strings.TrimPrefix(filter, "!"), "", true); err != nil {
			return questionSideError(err.Error())
		}
	}
	slices.SortFunc(walk.files, func(a, b sideSearchFile) int { return strings.Compare(a.path, b.path) })
	var paths []string
	var hits []sideSearchHit
	var counts []sideSearchCount
	var output strings.Builder
	scannedBytes, matchedFiles, matchedLines, rows := 0, 0, 0, 0
	outputFull := false
	write := func(line string) bool {
		if output.Len()+len(line)+1 > sideSearchMaxOutput {
			walk.truncated = true
			outputFull = true
			return false
		}
		output.WriteString(line)
		output.WriteByte('\n')
		return true
	}
	for _, file := range walk.files {
		if ctx.Err() != nil {
			walk.truncated = true
			break
		}
		rel, _ := filepath.Rel(requested, file.path)
		if rel == "." {
			rel = filepath.Base(file.path)
		}
		rel = filepath.ToSlash(rel)
		if filter != "" {
			matched, _ := sideGlobMatch(strings.TrimPrefix(filter, "!"), rel, true)
			if matched == strings.HasPrefix(filter, "!") {
				continue
			}
		}
		if typePattern != "" {
			matched, _ := sideGlobMatch(typePattern, rel, true)
			if !matched {
				continue
			}
		}
		f, err := walk.root.Open(file.rel)
		if err != nil {
			walk.truncated = true
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > sideSearchMaxFileBytes {
			_ = f.Close()
			walk.truncated = true
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, sideSearchMaxFileBytes+1))
		_ = f.Close()
		scannedBytes += len(data)
		if scannedBytes > sideSearchMaxBytes {
			walk.truncated = true
			break
		}
		if err != nil || len(data) > sideSearchMaxFileBytes {
			walk.truncated = true
			continue
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			continue
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		matched := make(map[int]bool)
		if multiline {
			starts := []int{0}
			for index, value := range data {
				if value == '\n' {
					starts = append(starts, index+1)
				}
			}
			for _, span := range matcher.FindAllIndex(data, sideSearchMaxHits+1) {
				start := sort.Search(len(starts), func(index int) bool { return starts[index] > span[0] }) - 1
				last := max(span[0], span[1]-1)
				end := sort.Search(len(starts), func(index int) bool { return starts[index] > last }) - 1
				for line := start; line <= end && line < len(lines); line++ {
					matched[line] = true
				}
				if len(matched) > sideSearchMaxHits {
					walk.truncated = true
					break
				}
			}
		} else {
			for line, text := range lines {
				if matcher.MatchString(text) {
					matched[line] = true
					if mode == "files_with_matches" {
						break
					}
				}
				if len(matched) > sideSearchMaxHits {
					walk.truncated = true
					break
				}
			}
		}
		if len(matched) == 0 {
			continue
		}
		matchedFiles++
		matchedLines += len(matched)
		display := file.path
		if rel, err := filepath.Rel(cwd, file.path); err == nil && filepath.IsLocal(rel) {
			display = rel
		}
		if mode != "content" {
			rows++
			if rows <= offset || rows > offset+limit {
				continue
			}
			line := display
			if mode == "count" {
				line = fmt.Sprintf("%s:%d", display, len(matched))
			}
			if !write(line) {
				break
			}
			if mode == "count" {
				counts = append(counts, sideSearchCount{Path: display, Count: len(matched)})
			} else {
				paths = append(paths, display)
			}
			continue
		}
		for index, text := range lines {
			if !matched[index] {
				continue
			}
			rows++
			if rows <= offset || rows > offset+limit {
				continue
			}
			text = sideSearchLine(text)
			hit := sideSearchHit{Path: display, Line: index + 1, Text: text}
			for contextLine := max(0, index-before); contextLine <= min(len(lines)-1, index+after); contextLine++ {
				separator := "-"
				if contextLine == index {
					separator = ":"
				}
				line := display + separator
				if showLine {
					line += fmt.Sprintf("%d%s", contextLine+1, separator)
				}
				if !write(line + sideSearchLine(lines[contextLine])) {
					break
				}
			}
			if outputFull {
				break
			}
			hits = append(hits, hit)
		}
		if outputFull {
			break
		}
	}
	if rows > offset+limit {
		walk.truncated = true
	}
	text := strings.TrimSuffix(output.String(), "\n")
	if text == "" {
		text = "No matches found"
	}
	if walk.truncated {
		text += "\n[Results truncated by pagination or the confined search budget; narrow the path or pattern.]"
	}
	return toolapi.Result{
		Content: []types.Content{{Type: "text", Text: text}},
		Details: map[string]any{"mode": mode, "paths": paths, "hits": hits, "counts": counts, "files": matchedFiles, "matches": matchedLines, "truncated": walk.truncated, "offset": offset, "limit": limit, "context_before": before, "context_after": after, "engine": "confined"},
	}
}

func sideSearchLine(text string) string {
	text = strings.TrimSuffix(text, "\r")
	if utf8.RuneCountInString(text) <= 500 {
		return text
	}
	return string([]rune(text)[:500]) + "…"
}
