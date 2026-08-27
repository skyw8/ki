package server

import (
	"fmt"
	"log/slog"

	"ki/internal/extension"
	"ki/internal/toggles"
)

// disableExtensions disables broken extension packages and reconciles both
// extension managers so a failed package cannot be used again in this server.
func (s *Server) disableExtensions(names ...string) {
	if len(names) == 0 {
		return
	}
	s.toggleMu.Lock()
	f := toggles.Load(s.cfg.Home)
	changed := false
	for _, name := range names {
		if name == "" || !f.Extensions.Allowed(name) || containsString(f.Extensions.Disabled, name) {
			continue
		}
		f.Extensions.Disabled = append(f.Extensions.Disabled, name)
		changed = true
	}
	if changed {
		if err := toggles.Save(s.cfg.Home, f); err != nil {
			slog.Warn("disable failed extension", "err", err)
			changed = false
		}
	}
	s.toggleMu.Unlock()
	if !changed {
		return
	}

	// Re-read the catalog after changing the toggle so both the ordinary and
	// provider managers immediately drop the failed package.
	snapshot := s.resources.Scan("")
	if s.ext != nil {
		s.ext.Configure(snapshot.Extensions)
	}
	s.reloadProviderExtensions()
}

func (s *Server) disableManifestExtensions(descriptors []extension.Descriptor) error {
	var names []string
	var first error
	tg := toggles.Load(s.cfg.Home)
	for _, d := range descriptors {
		if d.Error == "" || !tg.Extensions.Allowed(d.Name) {
			continue
		}
		names = append(names, d.Name)
		if first == nil {
			first = fmt.Errorf("extension %q: %s", d.Name, d.Error)
		}
	}
	if len(names) > 0 {
		s.disableExtensions(names...)
	}
	return first
}

func (s *Server) reportManifestErrors(sessionID string, descriptors []extension.Descriptor) {
	tg := toggles.Load(s.cfg.Home)
	var names []string
	for _, d := range descriptors {
		if d.Error != "" && tg.Extensions.Allowed(d.Name) {
			names = append(names, d.Name)
		}
	}
	if len(names) > 0 {
		s.disableExtensions(names...)
	}
	for _, d := range descriptors {
		if d.Error != "" && tg.Extensions.Allowed(d.Name) {
			s.onExtensionError(sessionID, d.Name, "manifest", "manifest", d.Error)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
