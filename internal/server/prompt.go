package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"ki/internal/prompt"
	"ki/internal/resources"
)

const maxAppendSystemPromptBytes = 64 << 10

func (s *Server) getPromptAppend(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.promptAppendView(s.workspacePath(r.URL.Query().Get("workspaceId"))))
}

func (s *Server) promptAppendView(cwd string) map[string]any {
	snapshot := s.resources.Scan(cwd)
	items := make([]map[string]any, 0, 4+len(snapshot.ExtensionPrompts))
	items = append(items, map[string]any{
		"source":    "builtin",
		"editable":  false,
		"available": true,
		"text":      prompt.DefaultAppendSystemPrompt,
		"bytes":     len(prompt.DefaultAppendSystemPrompt),
	})
	onDisk := map[string]resources.AppendSystemPrompt{}
	for _, layer := range snapshot.AppendSystemPrompts {
		onDisk[layer.Source] = layer
	}
	for _, source := range []string{resources.AppendSourceGlobal, resources.AppendSourceProject} {
		path, available := resources.AppendSystemPromptPath(s.cfg.Home, cwd, source)
		item := map[string]any{
			"source":    source,
			"editable":  true,
			"available": available,
			"path":      path,
			"exists":    false,
			"text":      "",
			"bytes":     0,
		}
		if !available {
			reason := "workspace-required"
			if source == resources.AppendSourceGlobal {
				reason = "home-required"
			}
			item["reason"] = reason
		}
		if layer, ok := onDisk[source]; ok {
			item["exists"] = true
			item["text"] = layer.Text
			item["bytes"] = len(layer.Text)
		}
		items = append(items, item)
	}
	files := map[string][]string{}
	for _, d := range snapshot.Extensions {
		if !d.Enabled {
			continue
		}
		for _, rel := range d.PromptAppendFiles() {
			files[d.Name] = append(files[d.Name], filepath.Join(d.Path, rel))
		}
	}
	for _, layer := range snapshot.ExtensionPrompts {
		items = append(items, map[string]any{
			"source":    "extension",
			"editable":  false,
			"available": true,
			"name":      layer.ExtensionID,
			"paths":     files[layer.ExtensionID],
			"text":      layer.Text,
			"bytes":     len(layer.Text),
		})
	}
	return map[string]any{"items": items, "effective": prompt.AppendSection(snapshot)}
}

func (s *Server) putPromptAppend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	path, ok := s.appendSystemPromptTarget(w, r, body.Source)
	if !ok {
		return
	}
	if body.Text == "" {
		http.Error(w, "prompt text is empty; delete the file instead", http.StatusBadRequest)
		return
	}
	if len(body.Text) > maxAppendSystemPromptBytes {
		http.Error(w, fmt.Sprintf("prompt text exceeds %d bytes", maxAppendSystemPromptBytes), http.StatusRequestEntityTooLarge)
		return
	}
	if err := writePromptFileAtomic(path, body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reloadPromptResources()
	s.getPromptAppend(w, r)
}

func (s *Server) deletePromptAppend(w http.ResponseWriter, r *http.Request) {
	path, ok := s.appendSystemPromptTarget(w, r, r.URL.Query().Get("source"))
	if !ok {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reloadPromptResources()
	s.getPromptAppend(w, r)
}

func (s *Server) appendSystemPromptTarget(w http.ResponseWriter, r *http.Request, source string) (string, bool) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	path, ok := resources.AppendSystemPromptPath(s.cfg.Home, cwd, source)
	if ok && source == resources.AppendSourceProject {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			http.Error(w, "workspace directory is unavailable", http.StatusBadRequest)
			return "", false
		}
	}
	if ok {
		return path, true
	}
	if source == resources.AppendSourceProject {
		http.Error(w, errWorkspaceOrCWDRequired.Error(), http.StatusBadRequest)
		return "", false
	}
	http.Error(w, "unknown source", http.StatusBadRequest)
	return "", false
}

func (s *Server) reloadPromptResources() {
	//nolint:contextcheck // reload rewarm is process-owned via runtimeCtx
	s.Reload()
	s.publishInvalidation(scopeSessions)
}

func writePromptFileAtomic(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prompt dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".APPEND_SYSTEM-*.md")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
