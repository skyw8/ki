package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseWriter serializes heartbeats and events and bounds a stopped reader's
// socket write. A global HTTP WriteTimeout would instead kill healthy long runs.
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
	// Cancellation can fire between the first check and installing the deadline.
	// Never replace its immediate deadline with another full write window.
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

// Cancel a blocked write without waiting for the heartbeat to acquire the same
// write mutex. Join the callback before the handler stops owning its writer.
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
		// net/http still has to write its final chunk after ServeHTTP returns.
		// A cancellation deadline left installed turns a clean EOF into truncation.
		_ = s.control.SetWriteDeadline(time.Time{})
	}
}
