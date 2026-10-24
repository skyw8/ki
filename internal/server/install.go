package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"ki/internal/extension"
)

type installState struct {
	state string
	err   string
}

type extensionInstaller struct {
	mu      sync.Mutex
	items   map[string]*installState
	running map[string]bool
}

func newExtensionInstaller() *extensionInstaller {
	return &extensionInstaller{items: map[string]*installState{}, running: map[string]bool{}}
}

func (in *extensionInstaller) get(name string) (installState, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	state, ok := in.items[name]
	if !ok {
		return installState{}, false
	}
	return *state, true
}

func (in *extensionInstaller) begin(name string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.running[name] {
		return false
	}
	in.running[name] = true
	in.items[name] = &installState{state: extension.StageDownload}
	return true
}

func (in *extensionInstaller) set(name, state, errText string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.items[name] = &installState{state: state, err: errText}
	if state == "done" || state == "failed" {
		delete(in.running, name)
	}
}

func (s *Server) installer() extension.Installer {
	return extension.Installer{
		Home:    s.cfg.Home,
		Source:  s.cfg.Extensions.Source,
		Release: s.cfg.Extensions.Release,
	}
}

func (s *Server) getAvailableExtensions(w http.ResponseWriter, _ *http.Request) {
	items := s.installer().List()
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{
			"name":            item.Name,
			"version":         item.Version,
			"description":     item.Description,
			"capabilities":    item.Capabilities,
			"asset":           item.Asset,
			"installed":       item.Installed,
			"updateAvailable": item.UpdateAvailable,
		}
		if item.InstalledVersion != "" {
			row["installedVersion"] = item.InstalledVersion
		}
		if item.Linked {
			row["linked"] = true
		}
		if state, ok := s.extInstall.get(item.Name); ok {
			row["install"] = map[string]any{"state": state.state, "error": state.err}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      out,
		"release":    s.cfg.Extensions.Release,
		"source":     s.cfg.Extensions.Source,
		"latest":     strings.HasPrefix(s.cfg.Extensions.Release, "latest/"),
		"linkedRoot": s.installer().LinkedRoot(),
	})
}

func (s *Server) installExtension(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	installer := s.installer()
	available := installer.List()
	found := false
	for _, item := range available {
		if item.Name == name {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "extension is not in the install catalog", http.StatusNotFound)
		return
	}
	if installer.LinkedRoot() {
		http.Error(w, "KI_HOME/extensions is a symlink to a checkout; install into a real directory", http.StatusConflict)
		return
	}
	if installer.Release == "" {
		http.Error(w, "no extension release is configured", http.StatusConflict)
		return
	}
	if !s.extInstall.begin(name) {
		state, _ := s.extInstall.get(name)
		writeJSON(w, http.StatusAccepted, map[string]any{"name": name, "state": state.state})
		return
	}
	s.publishInvalidation(scopeExtensions)
	go s.runInstall(name)
	writeJSON(w, http.StatusAccepted, map[string]any{"name": name, "state": extension.StageDownload})
}

func (s *Server) runInstall(name string) {
	installer := s.installer()
	installer.OnStage = func(stage string) {
		s.extInstall.set(name, stage, "")
		s.publishInvalidation(scopeExtensions)
	}
	_ = extension.CleanStaging(s.cfg.Home)
	ctx, cancel := context.WithTimeout(s.runtimeCtx, 30*time.Minute)
	defer cancel()
	if _, err := installer.Install(ctx, name); err != nil {
		s.extInstall.set(name, "failed", err.Error())
	} else {
		s.extInstall.set(name, "done", "")
	}
	s.publishInvalidation(scopeExtensions)
	if state, ok := s.extInstall.get(name); !ok || state.state != "done" {
		return
	}
	s.Reload()
}
