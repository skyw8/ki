package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type stalledWriter struct {
	*httptest.ResponseRecorder
	mu      sync.Mutex
	timer   *time.Timer
	expired chan struct{}
	once    sync.Once
}

func (w *stalledWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	if !deadline.IsZero() {
		w.timer = time.AfterFunc(time.Until(deadline), func() { w.once.Do(func() { close(w.expired) }) })
	}
	return nil
}

func (w *stalledWriter) Write([]byte) (int, error) {
	<-w.expired
	return 0, os.ErrDeadlineExceeded
}

func TestSSEWriterBoundsStalledReadersAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[cancelEarly], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := &stalledWriter{ResponseRecorder: httptest.NewRecorder(), expired: make(chan struct{})}
				// Exercise ResponseController through the actual gzip wrapper.
				writer := newSSEWriter(&gzipWriter{ResponseWriter: w, status: 200})
				writer.timeout = 30 * time.Second
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				defer writer.watch(ctx)()
				if cancelEarly {
					go func() { time.Sleep(time.Second); cancel() }()
				}
				start := time.Now()
				err := writer.write("data: x\n\n")
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatal(err)
				}
				want := 30 * time.Second
				if cancelEarly {
					want = time.Second
				}
				if time.Since(start) != want {
					t.Fatalf("write blocked for %v", time.Since(start))
				}
				if w.Header().Get("X-Accel-Buffering") != "no" || w.Header().Get("Content-Encoding") != "" {
					t.Fatal("SSE headers lost through compression wrapper")
				}
				cancel()
				if err := writer.write("data: late\n\n"); !errors.Is(err, context.Canceled) {
					t.Fatalf("write after cancellation: %v", err)
				}
			})
		})
	}
}
