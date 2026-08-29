package extension

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

func buildProviderSidecar(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "provider-sidecar")
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "testdata", "extensions", "provider")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // builds the local provider sidecar test fixture
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func providerTestDescriptor(t *testing.T, bin string) (Descriptor, provider.ExtensionProviderSpec) {
	t.Helper()
	root := t.TempDir()
	spec := provider.ExtensionProviderSpec{
		ID: "fake-provider", Name: "Fake Provider", API: "fake-api", BaseURL: "http://127.0.0.1",
		Auth: provider.AuthSpec{Type: provider.AuthNone},
		Models: []provider.ModelSeed{
			{ID: "fast", ContextWindow: 4096, MaxTokens: 512, Input: []string{"text"}},
			{ID: "slow", ContextWindow: 4096, MaxTokens: 512, Input: []string{"text"}},
		},
	}
	d := Descriptor{
		Name: "fake-provider-extension", Path: root, Enabled: true,
		Capabilities: []string{string(CapProvider)}, Providers: []provider.ExtensionProviderSpec{spec},
		root: root,
		manifest: Manifest{
			Name: "fake-provider-extension", Capabilities: []string{string(CapProvider)}, Providers: []provider.ExtensionProviderSpec{spec},
			Runtime: RuntimeSpec{Kind: runtimeRPC, Command: bin},
		},
	}
	return d, spec
}

func TestProviderManagerStreamsConcurrentAndCancels(t *testing.T) {
	bin := buildProviderSidecar(t)
	pm := NewProviderManager(t.TempDir())
	d, spec := providerTestDescriptor(t, bin)
	marker := filepath.Join(t.TempDir(), "started")
	d.manifest.Runtime.Env = map[string]string{"KI_MARKER": marker}
	if err := pm.Replace([]Descriptor{d}); err != nil {
		t.Fatal(err)
	}
	if !pm.HasProvider(spec.ID) {
		t.Fatal("provider not registered")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider sidecar must start lazily, stat=%v", err)
	}
	if got := NewManager(t.TempDir(), nil).Prepare(context.Background(), "session", t.TempDir(), []Descriptor{d}); len(got) != 0 {
		t.Fatalf("provider descriptor must not become a session tool set: %v", got)
	}
	t.Cleanup(pm.Close)

	built, err := provider.BuildExtensionProvider(spec)
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]provider.Model{}
	for _, model := range built.Models {
		models[model.ID] = model
	}

	run := func(ctx context.Context, model provider.Model) (string, []string, error) {
		var mu sync.Mutex
		var events []string
		msg, err := pm.NewStreamer(model, provider.Credential{Type: provider.AuthNone}).Stream(ctx, loop.Request{
			Provider: model.Provider, Model: model.ID, API: model.API,
			Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hi"}}}},
		}, func(delta loop.AssistantDelta) error {
			mu.Lock()
			events = append(events, delta.Type)
			mu.Unlock()
			return nil
		})
		return msg.Text(), events, err
	}

	text, events, err := run(context.Background(), models["fast"])
	if err != nil || text != "hello fast" {
		t.Fatalf("first stream text=%q err=%v", text, err)
	}
	if len(events) != 4 || events[0] != "text_start" || events[1] != "text_delta" || events[2] != "text_delta" || events[3] != "text_end" {
		t.Fatalf("stream events=%v", events)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("sidecar did not start: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			text, _, err := run(context.Background(), models["fast"])
			if err != nil {
				errs <- fmt.Errorf("concurrent stream text=%q: %w", text, err)
			} else if text != "hello fast" {
				errs <- fmt.Errorf("concurrent stream text=%q: %w", text, errProviderStreamFailed)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, _, err = run(ctx, models["slow"])
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error=%v, want deadline exceeded", err)
	}
	if err := pm.Replace(nil); err != nil {
		t.Fatal(err)
	}
	if pm.HasProvider(spec.ID) {
		t.Fatal("removed provider remains registered")
	}
}

func TestProviderManagerAuthEvents(t *testing.T) {
	bin := buildProviderSidecar(t)
	pm := NewProviderManager(t.TempDir())
	d, spec := providerTestDescriptor(t, bin)
	spec.Auth = provider.AuthSpec{Type: provider.AuthOAuth, Name: "Fake OAuth", Subscription: true}
	d.Providers[0] = spec
	d.manifest.Providers[0] = spec
	if err := pm.Replace([]Descriptor{d}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pm.Close)
	events := make(chan ProviderAuthEvent, 1)
	pm.SetProviderAuthHandler(func(event ProviderAuthEvent) { events <- event })
	requestID, err := pm.StartAuth(context.Background(), spec.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.RequestID != requestID || event.Provider != spec.ID || event.Type != "completed" || event.Credential == nil {
			t.Fatalf("auth event=%+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auth event")
	}
	if err := pm.AuthInput(context.Background(), spec.ID, requestID, "code"); err != nil {
		t.Fatalf("auth input: %v", err)
	}
}
