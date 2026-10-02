package server

import (
	"fmt"

	"ki/internal/codemode"
)

func (s *Server) codeModeFor(id string) (*codemode.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeMu.Lock()
	closed := s.runtimeClosed || s.deleting[id] || s.stopping[id] > 0
	s.runtimeMu.Unlock()
	if closed {
		return nil, fmt.Errorf("code mode session is closing")
	}
	return s.codeMode.NewSession(s.runtimeCtx, id)
}
