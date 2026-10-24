package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type sseWriter struct {
	w       http.ResponseWriter
	control *http.ResponseController
	mu      sync.Mutex
	timeout time.Duration
	ctx     context.Context
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	return &sseWriter{w: w, control: http.NewResponseController(w), timeout: 30 * time.Second}
}

func (s *sseWriter) write(format string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil && s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if err := s.control.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	defer func() { _ = s.control.SetWriteDeadline(time.Time{}) }()
	if s.ctx != nil && s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if _, err := fmt.Fprintf(s.w, format, args...); err != nil {
		return err
	}
	err := s.control.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *sseWriter) watch(ctx context.Context) func() {
	s.ctx = ctx
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = s.control.SetWriteDeadline(time.Now())
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
		_ = s.control.SetWriteDeadline(time.Time{})
	}
}
