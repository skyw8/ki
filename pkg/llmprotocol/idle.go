package llmprotocol

import (
	"errors"
	"io"
	"time"
)

var errStreamIdle = errors.New("provider stream idle timeout")

// idleReader times actual blocked reads, not time spent parsing or emitting to
// downstream consumers. Heartbeat bytes count as activity even without text.
// Closing the HTTP body interrupts its pending Read without a leaked goroutine.
type idleReader struct {
	io.ReadCloser
	timeout time.Duration
}

func (r idleReader) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.ReadCloser.Read(p)
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		_ = r.ReadCloser.Close()
		close(expired)
	})
	n, err := r.ReadCloser.Read(p)
	if !timer.Stop() {
		<-expired
		return n, errStreamIdle
	}
	return n, err
}
