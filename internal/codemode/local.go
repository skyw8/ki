package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"ki/internal/idgen"
	"ki/internal/types"
)

type localSession struct {
	mu     sync.Mutex
	limits Limits
	cells  map[string]*cell
	store  map[string]json.RawMessage
	closed bool
}

type cell struct {
	session *localSession
	id      string
	req     ExecuteRequest
	cb      Callbacks
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	events  chan completion

	mu            sync.Mutex
	changed       chan struct{}
	output        []types.Content
	outputBytes   int
	yieldCount    uint64
	observedYield uint64
	observing     bool
	status        string
	errorText     string
	terminate     bool
	explicitStop  bool
}

// NewLocalSession is an in-process runtime, primarily for cheap contract tests.
// Production callers should use Manager so VM heap pressure is process-scoped.
func NewLocalSession(limits Limits) *Session {
	l := &localSession{limits: limits.normalized(), cells: make(map[string]*cell), store: make(map[string]json.RawMessage)}
	return &Session{backend: l}
}

func (s *localSession) execute(ctx context.Context, req ExecuteRequest, cb Callbacks) (Response, error) {
	if err := validateRequest(req, s.limits); err != nil {
		return Response{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Response{}, errors.New("code mode session is closed")
	}
	if len(s.cells) >= s.limits.MaxCells {
		s.mu.Unlock()
		return Response{}, errors.New("code mode cell limit reached; observe completed cells first")
	}
	active := 0
	for _, c := range s.cells {
		select {
		case <-c.done:
		default:
			active++
		}
	}
	if active >= s.limits.MaxConcurrentCells {
		s.mu.Unlock()
		return Response{}, errors.New("code mode concurrent cell limit reached")
	}
	snapshot := make(map[string]json.RawMessage, len(s.store))
	for k, v := range s.store {
		snapshot[k] = v
	}
	// Cell identities survive transcript replay; a restarted worker must not
	// reuse cell-1 and collide with earlier nested tool audit events.
	unique, err := idgen.NewV7()
	if err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	id := "cell-" + unique
	runCtx, cancel := context.WithTimeout(ctx, s.limits.MaxExecutionTime)
	c := &cell{session: s, id: id, req: req, cb: cb, ctx: runCtx, cancel: cancel, done: make(chan struct{}), events: make(chan completion, s.limits.MaxPendingToolCalls+s.limits.MaxTimers+2), changed: make(chan struct{}), status: "running"}
	s.cells[id] = c
	s.mu.Unlock()
	go c.run(snapshot)
	return c.observe(ctx, yieldTime(req.YieldTime, req.YieldTimeSet, s.limits), req.MaxOutputBytes, req.MaxOutputBytesSet)
}

func (s *localSession) wait(ctx context.Context, req WaitRequest) (Response, error) {
	if req.YieldTime < 0 || req.MaxOutputBytes < 0 {
		return Response{}, errors.New("negative code mode observation limit")
	}
	s.mu.Lock()
	c := s.cells[req.CellID]
	s.mu.Unlock()
	if c == nil {
		return Response{}, errors.New("unknown or closed code mode cell")
	}
	if req.Terminate {
		c.stop()
	}
	return c.observe(ctx, yieldTime(req.YieldTime, req.YieldTimeSet, s.limits), req.MaxOutputBytes, req.MaxOutputBytesSet)
}

func (s *localSession) stopAll() []*cell {
	s.mu.Lock()
	defer s.mu.Unlock()
	cells := make([]*cell, 0, len(s.cells))
	for _, c := range s.cells {
		cells = append(cells, c)
		c.stop()
	}
	return cells
}

func (s *localSession) terminateAll(ctx context.Context) error {
	cells := s.stopAll()
	var cleanupErr error
	for _, c := range cells {
		select {
		case <-c.done:
		case <-ctx.Done():
			cleanupErr = ctx.Err()
			// Cancellation changes the reported observation, not callback
			// ownership. Local callers must still join the work they admitted.
			<-c.done
		}
	}
	// Occupy closure must not leave terminal cells consuming admission slots.
	s.mu.Lock()
	for _, c := range cells {
		delete(s.cells, c.id)
	}
	s.mu.Unlock()
	return cleanupErr
}

func (s *localSession) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.limits.DrainTimeout+time.Second)
	defer cancel()
	err := s.terminateAll(ctx)
	s.mu.Lock()
	s.store = make(map[string]json.RawMessage)
	s.mu.Unlock()
	return err
}

func (s *localSession) commit(writes map[string]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("code mode session is closed")
	}
	total := 0
	keys := len(s.store)
	for k, v := range s.store {
		total += len(k) + len(v)
	}
	for k, v := range writes {
		if old, ok := s.store[k]; ok {
			total -= len(k) + len(old)
		} else {
			keys++
		}
		total += len(k) + len(v)
	}
	if total > s.limits.MaxStoreBytes || keys > s.limits.MaxStoreKeys {
		return errors.New("session store quota exceeded at commit")
	}
	for k, v := range writes {
		s.store[k] = v
	}
	return nil
}

func (c *cell) changedLocked() { close(c.changed); c.changed = make(chan struct{}) }

func (c *cell) appendOutput(item types.Content) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outputBytes+len(b) > c.session.limits.MaxOutputBytes {
		return errors.New("code mode output limit exceeded")
	}
	c.outputBytes += len(b)
	c.output = append(c.output, item)
	c.changedLocked()
	return nil
}

func (c *cell) yield() {
	c.mu.Lock()
	c.yieldCount++
	c.changedLocked()
	c.mu.Unlock()
}

func (c *cell) stop() {
	c.mu.Lock()
	c.explicitStop = true
	c.mu.Unlock()
	c.cancel()
}

func (c *cell) finish(status, message string, terminate bool) {
	c.mu.Lock()
	c.status, c.errorText, c.terminate = status, message, terminate
	close(c.done)
	c.changedLocked()
	c.mu.Unlock()
	c.cancel()
}

func (c *cell) observe(ctx context.Context, duration time.Duration, maxBytes int, maxSet bool) (Response, error) {
	if maxBytes == 0 && !maxSet {
		maxBytes = c.session.limits.DefaultOutputBytes
	}
	if maxBytes > c.session.limits.MaxOutputBytes {
		maxBytes = c.session.limits.MaxOutputBytes
	}
	c.mu.Lock()
	if c.observing {
		c.mu.Unlock()
		return Response{}, errors.New("cell already has an active observer")
	}
	c.observing = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.observing = false; c.mu.Unlock() }()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	expired := false
	for {
		c.mu.Lock()
		if c.status != "running" || c.yieldCount > c.observedYield || expired {
			c.observedYield = c.yieldCount
			resp := Response{CellID: c.id, Status: c.status, Error: c.errorText, Terminate: c.terminate, Content: []types.Content{}}
			budget := maxBytes
			for _, item := range c.output {
				// Image payloads are bounded by the cell/IPC budget, not the text
				// preview budget; slicing base64 would corrupt the image.
				if item.Type != "text" {
					resp.Content = append(resp.Content, item)
					continue
				}
				if maxBytes == 0 {
					if item.Text != "" {
						resp.Truncated = true
					}
					continue
				}
				if len(item.Text) <= budget {
					budget -= len(item.Text)
					resp.Content = append(resp.Content, item)
					continue
				}
				cut := budget
				for cut > 0 && !utf8.ValidString(item.Text[:cut]) {
					cut--
				}
				if cut > 0 {
					resp.Content = append(resp.Content, types.Content{Type: "text", Text: item.Text[:cut]})
				}
				budget = 0
				resp.Truncated = true
			}
			if resp.Truncated && maxBytes > 0 {
				resp.Content = append(resp.Content, types.Content{Type: "text", Text: fmt.Sprintf("[Code mode output truncated to %d text bytes; omitted text is not retained.]", maxBytes)})
			}
			c.output = nil
			terminal := c.status != "running"
			c.mu.Unlock()
			if terminal {
				c.session.mu.Lock()
				delete(c.session.cells, c.id)
				c.session.mu.Unlock()
			}
			return resp, nil
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
		case <-timer.C:
			expired = true
		case <-ctx.Done():
			c.stop()
			return Response{}, ctx.Err()
		}
	}
}

func boundedError(err error, maxBytes int) string {
	if err == nil {
		return ""
	}
	s := fmt.Sprint(err)
	if len(s) > maxBytes {
		s = s[:maxBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
