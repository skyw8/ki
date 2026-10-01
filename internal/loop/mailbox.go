package loop

import (
	"context"
	"ki/internal/types"
	"time"
)

// HasWork separates explicit tasks/user steer from QueueOnly context at turn end.
func (i *Inbox) HasWork() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, m := range i.pending {
		if !m.ContextOnly {
			return true
		}
	}
	return false
}
func (i *Inbox) TakeWork() []types.Message {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	work := []types.Message{}
	contextOnly := []types.Message{}
	for _, m := range i.pending {
		if m.ContextOnly {
			contextOnly = append(contextOnly, m)
		} else {
			work = append(work, m)
		}
	}
	i.pending = contextOnly
	return work
}

// Wait subscribes and checks under one lock; notifications may coalesce but messages cannot.
func (i *Inbox) Wait(ctx context.Context, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		i.mu.Lock()
		if len(i.pending) > 0 {
			i.mu.Unlock()
			return false, nil
		}
		if i.activity == nil {
			i.activity = make(chan struct{})
		}
		activity := i.activity
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return true, nil
		case <-activity:
		}
	}
}

func (i *Inbox) WakeReason() string {
	if i == nil {
		return "unavailable"
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, message := range i.pending {
		if !message.ContextOnly {
			return "user_input"
		}
	}
	return "mailbox"
}
