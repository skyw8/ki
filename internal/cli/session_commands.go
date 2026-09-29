package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ki/internal/config"
	"ki/internal/server"
	"ki/internal/session"
)

const sessionOutputSchemaVersion = 1

type outputFlags struct {
	format string
	json   bool
	jsonl  bool
}

func (f *outputFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.format, "format", "text", "output format: text, json, or jsonl")
	cmd.Flags().BoolVar(&f.json, "json", false, "output JSON")
	cmd.Flags().BoolVar(&f.jsonl, "jsonl", false, "output one JSON object per line")
}

func (f outputFlags) value() (string, error) {
	format := f.format
	if f.json {
		if f.jsonl || (format != "" && format != "text" && format != "json") {
			return "", fmt.Errorf("conflicting output formats")
		}
		format = "json"
	}
	if f.jsonl {
		if format != "" && format != "text" && format != "jsonl" {
			return "", fmt.Errorf("conflicting output formats")
		}
		format = "jsonl"
	}
	if format != "text" && format != "json" && format != "jsonl" {
		return "", fmt.Errorf("invalid output format %q", format)
	}
	return format, nil
}

func addSessionBrowseCommands(parent *cobra.Command) {
	parent.AddCommand(
		newSessionListCommand(),
		newSessionSearchCommand(),
		newSessionShowCommand(),
		newSessionTraceCommand(),
		newSessionInspectCommand(),
	)
}

func newSessionListCommand() *cobra.Command {
	var out outputFlags
	var cwd, model, parentID, since, until string
	var limit int
	var includeAgents bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := out.value()
			if err != nil {
				return err
			}
			return withConfig("client", nil, func(cfg config.Config) error {
				infos, err := loadSessionList(cfg)
				if err != nil {
					return err
				}
				filtered := make([]session.Info, 0, len(infos))
				for _, info := range infos {
					at := info.UpdatedAt
					if at == "" {
						at = info.Timestamp
					}
					if !includeAgents && info.ForkMode == session.ForkModeTree ||
						cwd != "" && filepath.Clean(info.CWD) != filepath.Clean(cwd) ||
						model != "" && info.Model != model ||
						parentID != "" && info.ParentSessionID != parentID ||
						since != "" && at < since || until != "" && at > until {
						continue
					}
					filtered = append(filtered, info)
					if limit > 0 && len(filtered) >= limit {
						break
					}
				}
				return writeSessionList(cmd.OutOrStdout(), format, filtered)
			})
		},
	}
	out.bind(cmd)
	cmd.Flags().StringVar(&cwd, "cwd", "", "filter by workspace path")
	cmd.Flags().StringVar(&model, "model", "", "filter by model")
	cmd.Flags().StringVar(&parentID, "parent", "", "filter by parent session")
	cmd.Flags().StringVar(&since, "since", "", "include sessions updated at or after RFC3339 time")
	cmd.Flags().StringVar(&until, "until", "", "include sessions updated at or before RFC3339 time")
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum sessions (0 means all)")
	cmd.Flags().BoolVar(&includeAgents, "include-agents", false, "include tree-mode agent sessions")
	return cmd
}

type cliSearchHit struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

func newSessionSearchCommand() *cobra.Command {
	var out outputFlags
	var limit int
	var includeAgents bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search session messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := out.value()
			if err != nil {
				return err
			}
			return withConfig("client", nil, func(cfg config.Config) error {
				hits, hasMore, err := loadSessionSearch(cfg, args[0], limit, includeAgents)
				if err != nil {
					return err
				}
				if format == "json" {
					return encodeJSON(cmd.OutOrStdout(), map[string]any{"schemaVersion": sessionOutputSchemaVersion, "items": hits, "hasMore": hasMore})
				}
				if format == "jsonl" {
					return encodeJSONLines(cmd.OutOrStdout(), hits)
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "ID\tMODEL\tTITLE\tSNIPPET")
				for _, hit := range hits {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", hit.ID, hit.Model, oneLine(hit.Title), oneLine(hit.Snippet))
				}
				return w.Flush()
			})
		},
	}
	out.bind(cmd)
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum matching sessions")
	cmd.Flags().BoolVar(&includeAgents, "include-agents", false, "include tree-mode agent sessions")
	return cmd
}

type sessionShowResult struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ID            string                `json:"id"`
	LeafID        string                `json:"leafId"`
	Entries       []session.Entry       `json:"entries"`
	CompactTurns  []session.CompactTurn `json:"compactTurns,omitempty"`
	HasMore       bool                  `json:"hasMore"`
	OldestID      string                `json:"oldestId,omitempty"`
}

func newSessionShowCommand() *cobra.Command {
	var out outputFlags
	var view, before, turn string
	var limit, keep, turns int
	var full, thinking, toolArgs, systemPrompt bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show session messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := out.value()
			if err != nil {
				return err
			}
			if view != "compact" && view != "detailed" {
				return fmt.Errorf("invalid view %q", view)
			}
			return withConfig("client", nil, func(cfg config.Config) error {
				result, err := loadSessionShow(cfg, args[0], view, before, turn, limit, keep, full)
				if err != nil {
					return err
				}
				if turns > 0 && len(result.CompactTurns) > turns {
					result.CompactTurns = result.CompactTurns[len(result.CompactTurns)-turns:]
					visible := map[string]bool{}
					for _, turn := range result.CompactTurns {
						for _, entryID := range turn.EntryIDs {
							visible[entryID] = true
						}
					}
					result.Entries = slices.DeleteFunc(result.Entries, func(entry session.Entry) bool {
						return !visible[entry.ID]
					})
				}
				if format == "json" {
					return encodeJSON(cmd.OutOrStdout(), result)
				}
				if format == "jsonl" {
					return encodeJSONLines(cmd.OutOrStdout(), result.Entries)
				}
				return writeTranscript(cmd.OutOrStdout(), result, thinking, toolArgs, systemPrompt)
			})
		},
	}
	out.bind(cmd)
	cmd.Flags().StringVar(&view, "view", "compact", "view: compact or detailed")
	cmd.Flags().StringVar(&before, "before", "", "show entries older than this entry")
	cmd.Flags().StringVar(&turn, "turn", "", "show one turn")
	cmd.Flags().IntVar(&limit, "limit", session.DefaultViewLimit, "maximum detailed entries")
	cmd.Flags().IntVar(&keep, "keep", 1, "visible replies per compact turn")
	cmd.Flags().IntVar(&turns, "turns", 4, "maximum compact turns")
	cmd.Flags().BoolVar(&full, "full", false, "do not slim large entry bodies")
	cmd.Flags().BoolVar(&thinking, "thinking", false, "include thinking summaries")
	cmd.Flags().BoolVar(&toolArgs, "tool-args", false, "include tool arguments")
	cmd.Flags().BoolVar(&systemPrompt, "system", false, "include request system prompts and tool schemas")
	return cmd
}

type sessionTraceResult struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ID            string               `json:"id"`
	LeafID        string               `json:"leafId"`
	Trace         []session.TraceEntry `json:"trace"`
	HasMore       bool                 `json:"hasMore"`
	OldestID      string               `json:"oldestId,omitempty"`
}

func newSessionTraceCommand() *cobra.Command {
	var out outputFlags
	var types, roles, tools, since, until, before string
	var failed, cacheMiss, all bool
	var contextLines, limit int
	cmd := &cobra.Command{
		Use:   "trace <id>",
		Short: "Show a diagnostic session timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := out.value()
			if err != nil {
				return err
			}
			filter := session.TraceFilter{
				Types: csv(types), Roles: csv(roles), Tools: csv(tools), FailedOnly: failed,
				CacheMisses: cacheMiss, Since: since, Until: until,
			}
			if all {
				limit = 0
			}
			return withConfig("client", nil, func(cfg config.Config) error {
				result, err := loadSessionTrace(cfg, args[0], filter, contextLines, before, limit)
				if err != nil {
					return err
				}
				if format == "json" {
					return encodeJSON(cmd.OutOrStdout(), result)
				}
				if format == "jsonl" {
					return encodeJSONLines(cmd.OutOrStdout(), result.Trace)
				}
				return writeTrace(cmd.OutOrStdout(), result.Trace)
			})
		},
	}
	out.bind(cmd)
	cmd.Flags().StringVar(&types, "type", "", "comma-separated event types")
	cmd.Flags().StringVar(&roles, "role", "", "comma-separated message roles")
	cmd.Flags().StringVar(&tools, "tool", "", "comma-separated tool names")
	cmd.Flags().BoolVar(&failed, "failed", false, "show failed tool results only")
	cmd.Flags().BoolVar(&cacheMiss, "cache-miss", false, "show cache misses only")
	cmd.Flags().StringVar(&since, "since", "", "include events at or after RFC3339 time")
	cmd.Flags().StringVar(&until, "until", "", "include events at or before RFC3339 time")
	cmd.Flags().IntVar(&contextLines, "context", 0, "include neighboring timeline rows")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum newest rows")
	cmd.Flags().StringVar(&before, "before", "", "show rows older than this entry")
	cmd.Flags().BoolVar(&all, "all", false, "show all matching rows")
	return cmd
}

type sessionInspectResult struct {
	SchemaVersion   int              `json:"schemaVersion"`
	ID              string           `json:"id"`
	LeafID          string           `json:"leafId"`
	CWD             string           `json:"cwd,omitempty"`
	Provider        string           `json:"provider,omitempty"`
	Model           string           `json:"model,omitempty"`
	ParentSessionID string           `json:"parentSessionId,omitempty"`
	Children        []session.Info   `json:"children,omitempty"`
	Analysis        session.Analysis `json:"analysis"`
}

func newSessionInspectCommand() *cobra.Command {
	var out outputFlags
	var cacheOnly, toolsOnly, contextOnly bool
	cmd := &cobra.Command{
		Use:   "inspect <id>",
		Short: "Summarize session diagnostics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := out.value()
			if err != nil {
				return err
			}
			return withConfig("client", nil, func(cfg config.Config) error {
				result, err := loadSessionInspect(cfg, args[0])
				if err != nil {
					return err
				}
				if format == "json" {
					return encodeJSON(cmd.OutOrStdout(), result)
				}
				if format == "jsonl" {
					return writeInspectJSONL(cmd.OutOrStdout(), result, cacheOnly, toolsOnly, contextOnly)
				}
				return writeInspect(cmd.OutOrStdout(), result, cacheOnly, toolsOnly, contextOnly)
			})
		},
	}
	out.bind(cmd)
	cmd.Flags().BoolVar(&cacheOnly, "cache", false, "show cache diagnostics")
	cmd.Flags().BoolVar(&toolsOnly, "tools", false, "show tool diagnostics")
	cmd.Flags().BoolVar(&contextOnly, "context", false, "show context diagnostics")
	return cmd
}

func loadSessionList(cfg config.Config) ([]session.Info, error) {
	var online []session.Info
	if ok, err := readOnlyServerGET(cfg, "/v1/sessions", &online); ok {
		return online, err
	}
	return session.List(cfg.Sessions.Root)
}

func loadSessionSearch(cfg config.Config, query string, limit int, includeAgents bool) ([]cliSearchHit, bool, error) {
	var online struct {
		Items   []cliSearchHit `json:"items"`
		HasMore bool           `json:"hasMore"`
	}
	path := "/v1/sessions/search?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit) +
		"&includeAgents=" + strconv.FormatBool(includeAgents)
	if ok, err := readOnlyServerGET(cfg, path, &online); ok {
		if limit > 0 && len(online.Items) > limit {
			online.Items = online.Items[:limit]
			online.HasMore = true
		}
		return online.Items, online.HasMore, err
	}
	hits, more, err := session.Search(cfg.Sessions.Root, session.SearchOptions{Query: query, Limit: limit, IncludeAgents: includeAgents})
	out := make([]cliSearchHit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, cliSearchHit{
			ID: hit.ID, Title: hit.Title, CWD: hit.CWD, Model: hit.Model, UpdatedAt: hit.UpdatedAt, Snippet: hit.Snippet,
		})
	}
	return out, more, err
}

func loadSessionShow(cfg config.Config, id, view, before, turn string, limit, keep int, full bool) (sessionShowResult, error) {
	query := url.Values{"view": {view}, "limit": {strconv.Itoa(limit)}, "keep": {strconv.Itoa(keep)}}
	if before != "" {
		query.Set("before", before)
	}
	if turn != "" {
		query.Set("turn", turn)
	}
	var online sessionShowResult
	if !full {
		if ok, err := readOnlyServerGET(cfg, "/v1/sessions/"+url.PathEscape(id)+"?"+query.Encode(), &online); ok {
			online.SchemaVersion = sessionOutputSchemaVersion
			online.ID = id
			return online, err
		}
	}
	sess, err := openSession(cfg, id)
	if err != nil {
		return sessionShowResult{}, err
	}
	defer func() { _ = sess.Close() }()
	entries, leaf := sess.Entries(), sess.LeafID()
	result := sessionShowResult{SchemaVersion: sessionOutputSchemaVersion, ID: id, LeafID: leaf}
	if view == "compact" {
		var page session.CompactPage
		if turn != "" {
			var found bool
			page, found = session.BuildCompactTurn(entries, leaf, turn, keep)
			if !found {
				return sessionShowResult{}, fmt.Errorf("turn not found")
			}
		} else {
			page = session.BuildCompact(entries, leaf, before, keep)
		}
		result.Entries, result.CompactTurns = page.Entries, page.Turns
		result.HasMore, result.OldestID = page.HasMore, page.OldestID
		return result, nil
	}
	if full {
		path := session.LeafChain(entries, leaf)
		if before != "" {
			at := indexEntry(path, before)
			if at < 0 {
				return sessionShowResult{}, fmt.Errorf("history cursor not found on active branch")
			}
			path = path[:at]
		}
		if limit > 0 && len(path) > limit {
			result.HasMore = true
			path = path[len(path)-limit:]
		}
		result.Entries = session.RedactProviderContext(path)
		if len(path) > 0 {
			result.OldestID = path[0].ID
		}
		return result, nil
	}
	if turn != "" {
		page, found := session.BuildTurn(entries, leaf, turn, before, limit)
		if !found {
			return sessionShowResult{}, fmt.Errorf("turn not found")
		}
		result.Entries, result.HasMore, result.OldestID = page.Entries, page.HasMore, page.OldestID
		return result, nil
	}
	if before != "" {
		page := session.BuildBefore(entries, leaf, before, limit)
		result.Entries, result.HasMore, result.OldestID = page.Entries, page.HasMore, page.OldestID
		return result, nil
	}
	page := session.BuildTail(entries, leaf, limit, true)
	result.Entries, result.HasMore, result.OldestID = page.Entries, page.HasMore, page.OldestID
	return result, nil
}

func loadSessionTrace(cfg config.Config, id string, filter session.TraceFilter, contextLines int, before string, limit int) (sessionTraceResult, error) {
	query := url.Values{"view": {"trace"}, "context": {strconv.Itoa(contextLines)}}
	setCSVQuery(query, "type", filter.Types)
	setCSVQuery(query, "role", filter.Roles)
	setCSVQuery(query, "tool", filter.Tools)
	query.Set("failed", strconv.FormatBool(filter.FailedOnly))
	query.Set("cacheMiss", strconv.FormatBool(filter.CacheMisses))
	query.Set("since", filter.Since)
	query.Set("until", filter.Until)
	if before != "" {
		query.Set("before", before)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	} else {
		query.Set("limit", strconv.Itoa(session.MaxViewEntries))
	}
	var online sessionTraceResult
	endpoint := "/v1/sessions/" + url.PathEscape(id) + "?" + query.Encode()
	if ok, err := readOnlyServerGET(cfg, endpoint, &online); ok {
		if err != nil {
			return sessionTraceResult{}, err
		}
		// Older servers treated unknown views as detailed and returned no trace
		// envelope. Fall back to the local reader during rolling upgrades.
		if online.ID != "" && online.Trace != nil {
			if limit > 0 {
				online.SchemaVersion = sessionOutputSchemaVersion
				return online, nil
			}
			// --all remains one CLI operation but follows the server's bounded
			// pages so a long trace never creates an unbounded HTTP response.
			for online.HasMore && online.OldestID != "" {
				query.Set("before", online.OldestID)
				var older sessionTraceResult
				if _, err := readOnlyServerGET(cfg, "/v1/sessions/"+url.PathEscape(id)+"?"+query.Encode(), &older); err != nil {
					return sessionTraceResult{}, err
				}
				online.Trace = append(older.Trace, online.Trace...)
				online.HasMore, online.OldestID = older.HasMore, older.OldestID
			}
			online.SchemaVersion = sessionOutputSchemaVersion
			return online, nil
		}
	}
	sess, err := openSession(cfg, id)
	if err != nil {
		return sessionTraceResult{}, err
	}
	defer func() { _ = sess.Close() }()
	rows := session.TraceWithContext(sess.Entries(), sess.LeafID(), filter, contextLines)
	if before != "" {
		at := -1
		for i := range rows {
			if rows[i].ID == before {
				at = i
				break
			}
		}
		if at < 0 {
			return sessionTraceResult{}, fmt.Errorf("trace cursor not found on active branch")
		}
		rows = rows[:at]
	}
	more := limit > 0 && len(rows) > limit
	if more {
		rows = rows[len(rows)-limit:]
	}
	oldest := ""
	if len(rows) > 0 {
		oldest = rows[0].ID
	}
	return sessionTraceResult{
		SchemaVersion: sessionOutputSchemaVersion, ID: id, LeafID: sess.LeafID(),
		Trace: rows, HasMore: more, OldestID: oldest,
	}, nil
}

func loadSessionInspect(cfg config.Config, id string) (sessionInspectResult, error) {
	var online struct {
		ID              string            `json:"id"`
		LeafID          string            `json:"leafId"`
		CWD             string            `json:"cwd"`
		Provider        string            `json:"provider"`
		Model           string            `json:"model"`
		ParentSessionID string            `json:"parentSessionId"`
		Children        []session.Info    `json:"children"`
		Analysis        *session.Analysis `json:"analysis"`
	}
	if ok, err := readOnlyServerGET(cfg, "/v1/sessions/"+url.PathEscape(id)+"?view=inspect", &online); ok {
		if err != nil {
			return sessionInspectResult{}, err
		}
		// See loadSessionTrace: an old server has no inspect envelope.
		if online.ID != "" && online.Analysis != nil {
			return sessionInspectResult{
				SchemaVersion: sessionOutputSchemaVersion, ID: online.ID, LeafID: online.LeafID,
				CWD: online.CWD, Provider: online.Provider, Model: online.Model,
				ParentSessionID: online.ParentSessionID, Children: online.Children, Analysis: *online.Analysis,
			}, nil
		}
	}
	sess, err := openSession(cfg, id)
	if err != nil {
		return sessionInspectResult{}, err
	}
	defer func() { _ = sess.Close() }()
	infos, err := session.List(cfg.Sessions.Root)
	if err != nil {
		return sessionInspectResult{}, err
	}
	var children []session.Info
	for _, info := range infos {
		if info.ParentSessionID == id {
			children = append(children, info)
		}
	}
	return sessionInspectResult{
		SchemaVersion: sessionOutputSchemaVersion, ID: id, LeafID: sess.LeafID(),
		CWD: sess.Header.CWD, Provider: sess.Config.Provider, Model: sess.Config.Model,
		ParentSessionID: sess.Header.ParentSession, Children: children,
		Analysis: session.Analyze(sess.Entries(), sess.LeafID()),
	}, nil
}

func readOnlyServerGET(cfg config.Config, path string, out any) (bool, error) {
	sf, err := server.ReadServerFile(cfg.Home)
	if err != nil || sf.Addr == "" || !ping("http://"+sf.Addr, sf.Token) {
		return false, nil
	}
	err = doJSONContext(context.Background(), "http://"+sf.Addr, sf.Token, http.MethodGet, path, nil, out)
	return true, err
}

func openSession(cfg config.Config, id string) (*session.Session, error) {
	dir, err := session.Find(cfg.Sessions.Root, id)
	if err != nil {
		return nil, err
	}
	return session.Open(dir)
}

func writeSessionList(w io.Writer, format string, infos []session.Info) error {
	if format == "json" {
		return encodeJSON(w, map[string]any{"schemaVersion": sessionOutputSchemaVersion, "items": infos})
	}
	if format == "jsonl" {
		return encodeJSONLines(w, infos)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tUPDATED\tMODEL\tTITLE")
	for _, info := range infos {
		at := info.UpdatedAt
		if at == "" {
			at = info.Timestamp
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", info.ID, at, info.Model, oneLine(info.Title))
	}
	return tw.Flush()
}

func writeTranscript(w io.Writer, result sessionShowResult, showThinking, showToolArgs, showSystem bool) error {
	for _, entry := range result.Entries {
		if entry.Type == "request_header" {
			if showSystem {
				_, _ = fmt.Fprintf(w, "[%s] request %s/%s\n%s\n\n", entry.Timestamp, entry.Provider, entry.ModelID, entry.System)
			}
			continue
		}
		if entry.Type == "compaction" {
			_, _ = fmt.Fprintf(w, "[%s] compaction\n%s\n\n", entry.Timestamp, entry.Summary)
			continue
		}
		if entry.Message == nil {
			continue
		}
		m := entry.Message
		_, _ = fmt.Fprintf(w, "[%s] %s", entry.Timestamp, m.Role)
		if m.ToolName != "" {
			_, _ = fmt.Fprintf(w, " %s", m.ToolName)
		}
		if m.IsError {
			_, _ = fmt.Fprint(w, " ERROR")
		}
		_, _ = fmt.Fprintln(w)
		for _, content := range m.Content {
			switch content.Type {
			case "text", "":
				_, _ = fmt.Fprintln(w, content.Text)
			case "thinking":
				if showThinking {
					_, _ = fmt.Fprintln(w, content.Thinking)
				}
			case "toolCall":
				_, _ = fmt.Fprintf(w, "→ %s", content.Name)
				if showToolArgs {
					b, _ := json.Marshal(content.Arguments)
					_, _ = fmt.Fprintf(w, " %s", b)
				}
				_, _ = fmt.Fprintln(w)
			}
		}
		_, _ = fmt.Fprintln(w)
	}
	for _, turn := range result.CompactTurns {
		if turn.HiddenCount > 0 {
			_, _ = fmt.Fprintf(w, "[folded turn %s: %d hidden, %d steps, %d cache misses]\n",
				turn.ID, turn.HiddenCount, turn.Stats.Steps, turn.Stats.CacheMisses)
		}
	}
	if result.HasMore {
		_, _ = fmt.Fprintf(w, "[more before %s]\n", result.OldestID)
	}
	return nil
}

func writeTrace(w io.Writer, rows []session.TraceEntry) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tTYPE\tROLE/TOOL\tINPUT\tCACHE_READ\tSTATUS\tPREVIEW")
	for _, row := range rows {
		name := row.Role
		if len(row.ToolNames) > 0 {
			name += "/" + strings.Join(row.ToolNames, ",")
		}
		input, read := 0, 0
		if row.Usage != nil {
			input, read = row.Usage.Input, row.Usage.CacheRead
		}
		status := "ok"
		if row.Failed {
			status = "failed"
		} else if row.CacheMiss != nil {
			status = fmt.Sprintf("cache-miss:%d%%", session.CacheMissPercent(row.CacheMiss))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			row.Timestamp, row.Type, name, input, read, status, oneLine(row.Preview))
	}
	return tw.Flush()
}

func writeInspect(w io.Writer, result sessionInspectResult, cacheOnly, toolsOnly, contextOnly bool) error {
	a := result.Analysis
	all := !cacheOnly && !toolsOnly && !contextOnly
	if all {
		_, _ = fmt.Fprintf(w, "session=%s provider=%s model=%s cwd=%s parent=%s children=%d\n",
			result.ID, result.Provider, result.Model, result.CWD, result.ParentSessionID, len(result.Children))
		_, _ = fmt.Fprintf(w, "entries=%d user_turns=%d assistant_steps=%d compactions=%d\n",
			a.Entries, a.UserTurns, a.AssistantSteps, len(a.Compactions))
	}
	if all || cacheOnly {
		_, _ = fmt.Fprintf(w, "tokens input=%d output=%d cache_read=%d cache_write=%d\n",
			a.InputTokens, a.OutputTokens, a.CacheReadTokens, a.CacheWriteTokens)
		for _, row := range a.CacheMisses {
			_, _ = fmt.Fprintf(w, "cache-miss %s missed=%d ratio=%d%% prompt=%d previous=%d\n",
				row.Timestamp, row.CacheMiss.MissedTokens, session.CacheMissPercent(row.CacheMiss),
				row.CacheMiss.Prompt, row.CacheMiss.Previous)
		}
	}
	if all || toolsOnly {
		_, _ = fmt.Fprintf(w, "tools calls=%d failures=%d\n", a.ToolCalls, len(a.ToolFailures))
		for _, failure := range a.ToolFailures {
			_, _ = fmt.Fprintf(w, "tool-failure %s %s %s\n", failure.Timestamp, failure.Tool, oneLine(failure.Error))
		}
	}
	if all || contextOnly {
		_, _ = fmt.Fprintf(w, "context max_used=%d window=%d prompt_changes=%d\n",
			a.MaxUsedTokens, a.ContextWindow, len(a.PromptChanges))
		for _, change := range a.PromptChanges {
			_, _ = fmt.Fprintf(w, "prompt-change %s system=%s tools=%s\n", change.Timestamp, change.SystemHash, change.ToolsHash)
		}
	}
	return nil
}

func writeInspectJSONL(w io.Writer, result sessionInspectResult, cacheOnly, toolsOnly, contextOnly bool) error {
	a := result.Analysis
	all := !cacheOnly && !toolsOnly && !contextOnly
	enc := json.NewEncoder(w)
	if all || cacheOnly {
		for _, row := range a.CacheMisses {
			if err := enc.Encode(map[string]any{"type": "cacheMiss", "entry": row}); err != nil {
				return err
			}
		}
	}
	if all || toolsOnly {
		for _, failure := range a.ToolFailures {
			if err := enc.Encode(map[string]any{"type": "toolFailure", "failure": failure}); err != nil {
				return err
			}
		}
	}
	if all || contextOnly {
		for _, change := range a.PromptChanges {
			if err := enc.Encode(map[string]any{"type": "promptChange", "change": change}); err != nil {
				return err
			}
		}
	}
	return enc.Encode(map[string]any{"type": "summary", "session": result})
}

func encodeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func encodeJSONLines[T any](w io.Writer, rows []T) error {
	enc := json.NewEncoder(w)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func csv(raw string) []string {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func setCSVQuery(q url.Values, key string, values []string) {
	if len(values) > 0 {
		q.Set(key, strings.Join(values, ","))
	}
}

func indexEntry(entries []session.Entry, id string) int {
	for i := range entries {
		if entries[i].ID == id {
			return i
		}
	}
	return -1
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		return r
	}, s)), " ")
}
