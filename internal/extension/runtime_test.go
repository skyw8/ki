package extension

import (
	"context"
	"slices"
	"testing"
	"time"
)

func runtimeDescriptor(version string) Descriptor {
	return Descriptor{
		Name: "runtime", Version: version, Enabled: true,
		manifest: Manifest{Runtime: RuntimeSpec{Kind: runtimeRPC, Command: "unused"}},
	}
}

func runtimeTestClient() *rpcClient {
	return &rpcClient{closed: make(chan struct{})}
}

func receiveRuntime[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("runtime state did not arrive")
		var zero T
		return zero
	}
}

func TestRuntimeRetryFastExitsAndRecovery(t *testing.T) {
	var retry runtimeRetry
	for i, want := range []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second} {
		if got := retry.afterExit(time.Millisecond); got != want {
			t.Fatalf("exit %d: delay = %v, want %v", i, got, want)
		}
	}
	for range 100 {
		if got := retry.afterExit(time.Millisecond); got != 30*time.Second || retry.fastExits != 7 {
			t.Fatalf("backoff did not saturate: %v, count %d", got, retry.fastExits)
		}
	}
	if got := retry.afterExit(runtimeStableUptime); got != 0 || retry.fastExits != 0 {
		t.Fatalf("sustained uptime did not reset retries: %v, count %d", got, retry.fastExits)
	}
	if got := retry.afterExit(time.Millisecond); got != 0 {
		t.Fatalf("first crash after recovery was delayed: %v", got)
	}
	if got := retry.afterExit(time.Millisecond); got != time.Second {
		t.Fatalf("second crash after recovery = %v", got)
	}
}

func TestWatchRuntimeRetryInterrupted(t *testing.T) {
	for _, action := range []string{"reload", "disable", "shutdown", "initialize-failure"} {
		t.Run(action, func(t *testing.T) {
			m := NewManager("", nil)
			d := runtimeDescriptor("1")
			m.Configure([]Descriptor{d})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.runtimeCtx, m.runtimeCancel = ctx, cancel
			done := make(chan struct{})
			m.watching[d.Name] = done
			type attempt struct {
				d Descriptor
				c *rpcClient
			}
			attempts := make(chan attempt, 8)
			delays := make(chan time.Duration, 8)
			hooks := runtimeHooks{
				now: time.Now,
				start: func(_ context.Context, _ string, d Descriptor) *rpcClient {
					if action == "initialize-failure" && d.Version == "1" {
						attempts <- attempt{d: d}
						return nil
					}
					c := runtimeTestClient()
					if d.Version == "1" {
						c.markClosed()
					}
					attempts <- attempt{d: d, c: c}
					return c
				},
				wait: func(ctx context.Context, changed <-chan struct{}, delay time.Duration) bool {
					delays <- delay
					// The real wait/select is exercised, but only a catalog
					// notification or cancellation can release this fake clock.
					return waitRuntimeChange(ctx, changed, time.Hour)
				},
			}
			go m.watchRuntime(ctx, d, done, hooks)
			first := receiveRuntime(t, attempts)
			if first.d.Version != "1" {
				t.Fatalf("first launch = %s", first.d.Version)
			}
			wantDelay := time.Second
			if action == "initialize-failure" {
				wantDelay = 2 * time.Second
			} else {
				receiveRuntime(t, attempts) // first crash retries promptly
			}
			if got := receiveRuntime(t, delays); got > wantDelay || got < wantDelay-100*time.Millisecond {
				t.Fatalf("retry delay = %v, want approximately %v", got, wantDelay)
			}
			switch action {
			case "reload", "initialize-failure":
				next := runtimeDescriptor("2")
				m.Configure([]Descriptor{next})
				restarted := receiveRuntime(t, attempts)
				if restarted.d.Version != "2" {
					t.Fatalf("reload launched stale descriptor %s", restarted.d.Version)
				}
				// A changed runtime's first fast crash should retry immediately,
				// independent of the old descriptor's accumulated crash history.
				restarted.c.markClosed()
				retried := receiveRuntime(t, attempts)
				if retried.d.Version != "2" {
					t.Fatalf("retry launched stale descriptor %s", retried.d.Version)
				}
				m.Configure(nil)
				receiveRuntime(t, retried.c.closed)
			case "disable":
				m.Configure(nil)
			case "shutdown":
				m.Close()
			}
			receiveRuntime(t, done)
			if statuses := m.RuntimeStatuses(); len(statuses) != 0 {
				t.Fatalf("disabled runtime retained statuses: %v", statuses)
			}
			select {
			case unexpected := <-attempts:
				t.Fatalf("unexpected launch after stop: %s", unexpected.d.Version)
			default:
			}
		})
	}
}

func TestRuntimeRetryIgnoresUnrelatedCatalogChanges(t *testing.T) {
	m := NewManager("", nil)
	d := runtimeDescriptor("1")
	m.Configure([]Descriptor{d})
	now := time.Unix(1, 0)
	var delays []time.Duration
	hooks := runtimeHooks{
		now: func() time.Time { return now },
		wait: func(_ context.Context, _ <-chan struct{}, delay time.Duration) bool {
			delays = append(delays, delay)
			if len(delays) == 1 {
				other := runtimeDescriptor("1")
				other.Name = "other"
				m.Configure([]Descriptor{d, other})
				now = now.Add(250 * time.Millisecond)
			} else {
				now = now.Add(delay)
			}
			return true
		},
	}
	if !m.waitRuntimeRetry(t.Context(), d, m.changed, time.Second, hooks) {
		t.Fatal("retry stopped unexpectedly")
	}
	if !slices.Equal(delays, []time.Duration{time.Second, 750 * time.Millisecond}) {
		t.Fatalf("unrelated reload bypassed remaining delay: %v", delays)
	}
}

func TestPublishRuntimeRejectsStaleLaunch(t *testing.T) {
	for _, action := range []string{"disable", "replace", "replace-back", "close", "cancel"} {
		t.Run(action, func(t *testing.T) {
			m := NewManager("", nil)
			d := runtimeDescriptor("1")
			m.Configure([]Descriptor{d})
			generation := m.generation
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch action {
			case "disable":
				m.Configure(nil)
			case "replace":
				m.Configure([]Descriptor{runtimeDescriptor("2")})
			case "replace-back":
				m.Configure([]Descriptor{runtimeDescriptor("2")})
				m.Configure([]Descriptor{d})
			case "close":
				m.Close()
			case "cancel":
				cancel()
			}
			c := runtimeTestClient()
			if got := m.publishRuntime(ctx, "", d, generation, c); got != nil {
				t.Fatal("stale runtime was published")
			}
			receiveRuntime(t, c.closed)
			if len(m.by) != 0 {
				t.Fatal("stale runtime retained in manager")
			}
		})
	}
}

func TestPublishRuntimeAllowsDirectAndUnchangedCatalog(t *testing.T) {
	for _, configured := range []bool{false, true} {
		m := NewManager("", nil)
		d := runtimeDescriptor("1")
		if configured {
			m.Configure([]Descriptor{d})
		}
		generation := m.generation
		if configured {
			m.Configure([]Descriptor{d})
		}
		c := runtimeTestClient()
		if got := m.publishRuntime(t.Context(), "", d, generation, c); got != c || m.by[d.Name] != c {
			t.Fatalf("valid launch rejected (configured=%v)", configured)
		}
		m.Close()
		receiveRuntime(t, c.closed)
	}
}

func TestSnapshotAdoptionAndAuthoritativeRemoval(t *testing.T) {
	for _, action := range []string{"publish", "configure-empty", "configure-enabled", "close"} {
		t.Run(action, func(t *testing.T) {
			m := NewManager("", nil)
			m.Configure(nil)
			d := runtimeDescriptor("1")
			m.mu.Lock()
			m.adoptSnapshotLocked(d)
			admitted := m.desiredLocked(d)
			generation := m.generation
			m.mu.Unlock()
			if !admitted {
				t.Fatal("newly discovered enabled snapshot was not admitted")
			}
			switch action {
			case "configure-empty":
				m.Configure(nil)
			case "configure-enabled":
				m.Configure([]Descriptor{d})
			case "close":
				m.Close()
			}
			c := runtimeTestClient()
			got := m.publishRuntime(t.Context(), "", d, generation, c)
			if action == "publish" {
				if got != c {
					t.Fatal("valid snapshot launch was rejected")
				}
				m.Close()
			} else if got != nil {
				t.Fatal("authoritative configure/close retained in-flight snapshot launch")
			}
			receiveRuntime(t, c.closed)
			if action == "configure-empty" || action == "close" {
				m.mu.Lock()
				m.adoptSnapshotLocked(d)
				resurrected := m.desiredLocked(d)
				m.mu.Unlock()
				if resurrected {
					t.Fatal("stale snapshot resurrected a removed package")
				}
			}
		})
	}
}

func TestSnapshotCannotAdoptKnownDisabledPackage(t *testing.T) {
	for _, previouslyEnabled := range []bool{false, true} {
		m := NewManager("", nil)
		d := runtimeDescriptor("1")
		if previouslyEnabled {
			m.Configure([]Descriptor{d})
			m.Configure(nil)
		} else {
			disabled := d
			disabled.Enabled = false
			m.Configure([]Descriptor{disabled})
		}
		m.mu.Lock()
		m.adoptSnapshotLocked(d)
		admitted := m.desiredLocked(d)
		m.mu.Unlock()
		if admitted {
			t.Fatalf("stale enabled snapshot admitted known disabled package (previouslyEnabled=%v)", previouslyEnabled)
		}
	}
}
