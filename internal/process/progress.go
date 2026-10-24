package process

import "sync"

const maxProcessProgressUpdates = 10000

type processPublisher struct {
	mu        sync.Mutex
	wake      chan struct{}
	done      chan struct{}
	pending   *Update
	revision  uint64
	finished  bool
	remaining int
	listener  func(Update)
}

func newProcessPublisher(listener func(Update)) *processPublisher {
	return &processPublisher{
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		remaining: maxProcessProgressUpdates, listener: listener,
	}
}

func (p *processPublisher) enqueue(update Update) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || update.Process.Revision <= p.revision {
		return
	}
	p.revision = update.Process.Revision
	p.finished = update.Process.Status == "exited"
	if !p.finished && p.remaining == 0 {
		return
	}
	p.pending = &update
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *processPublisher) run(initial Snapshot) {
	defer close(p.done)
	p.listener(Update{Process: initial})
	for range p.wake {
		p.mu.Lock()
		update := p.pending
		p.pending = nil
		if update != nil && update.Process.Status != "exited" {
			p.remaining--
		}
		p.mu.Unlock()
		if update == nil {
			continue
		}
		p.listener(*update)
		if update.Process.Status == "exited" {
			return
		}
	}
}
