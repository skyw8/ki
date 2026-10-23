package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"ki/internal/server"
)

// TestHandshakeRecordsProcessIdentity pins the fields `ki status` and
// `ki restart` depend on. Build identity is what lets a client tell "the daemon
// answering me is the binary I just installed" apart from "an older daemon
// still owns the handshake".
func TestHandshakeRecordsProcessIdentity(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)
	if sf.PID <= 0 {
		t.Fatalf("server.json pid missing: %+v", sf)
	}
	if sf.Build == "" {
		t.Fatalf("server.json version missing: %+v", sf)
	}
	if sf.Exe == "" {
		t.Fatalf("server.json exe missing: %+v", sf)
	}
	if sf.StartedAt.IsZero() {
		t.Fatalf("server.json startedAt missing: %+v", sf)
	}
	if !pidAlive(sf.PID) {
		t.Fatalf("recorded pid %d is not alive", sf.PID)
	}
}

// TestStatusReportsRunningServer covers the text/JSON contract of `ki status`:
// it must describe the live daemon and stay read-only.
func TestStatusReportsRunningServer(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)

	out, code := runBin(t, home, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s", code, out)
	}
	var st struct {
		Running       bool   `json:"running"`
		Addr          string `json:"addr"`
		PID           int    `json:"pid"`
		Version       string `json:"version"`
		ClientVersion string `json:"clientVersion"`
		Mismatch      string `json:"mismatch"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	if !st.Running || st.Addr != sf.Addr || st.PID != sf.PID {
		t.Fatalf("status does not describe the live server: %+v (handshake %+v)", st, sf)
	}
	if st.Version != st.ClientVersion {
		t.Fatalf("same build reported different versions: %+v", st)
	}
	if st.Mismatch != "" {
		t.Fatalf("same build reported a mismatch: %q", st.Mismatch)
	}

	if !pingAddr(t, sf.Addr) {
		t.Fatal("status stopped the server")
	}
	if _, err := server.ReadServerFile(home); err != nil {
		t.Fatalf("status removed the handshake: %v", err)
	}
}

// TestStopEndsServerAndClearsHandshake covers what `ki stop` is for: owned
// cleanup, a released listener, no stale handshake, and idempotence.
func TestStopEndsServerAndClearsHandshake(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)

	out, code := runBin(t, home, "stop", "--json")
	if code != 0 {
		t.Fatalf("stop: %d %s", code, out)
	}
	var res struct {
		Stopped  bool `json:"stopped"`
		Graceful bool `json:"graceful"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stop json: %v\n%s", err, out)
	}
	if !res.Stopped || !res.Graceful {
		t.Fatalf("stop did not report a graceful stop: %+v", res)
	}
	if pingAddr(t, sf.Addr) {
		t.Fatal("server still answers after stop")
	}
	if pidAlive(sf.PID) {
		t.Fatalf("pid %d survived stop", sf.PID)
	}
	if _, err := server.ReadServerFile(home); !os.IsNotExist(err) {
		t.Fatalf("server.json not removed: %v", err)
	}

	// A second stop must be a no-op, not an error: scripts call it blindly.
	if out, code := runBin(t, home, "stop"); code != 0 {
		t.Fatalf("second stop: %d %s", code, out)
	}
}

// TestStopFallsBackToSignalForPreLifecycleServer covers the first upgrade over a
// daemon that has neither the shutdown route nor a pid in server.json — the
// migration path this feature exists for.
func TestStopFallsBackToSignalForPreLifecycleServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fallback identifies the listener through /proc or lsof")
	}
	home, _ := isolate(t)
	sf := startServe(t, home)

	// Simulate an old handshake: addr and token only.
	if err := server.WriteServerFile(home, server.File{Addr: sf.Addr, Token: sf.Token}); err != nil {
		t.Fatal(err)
	}
	out, code := runBin(t, home, "stop", "--timeout", "10s")
	if code != 0 {
		t.Fatalf("stop: %d %s", code, out)
	}
	if pingAddr(t, sf.Addr) {
		t.Fatal("pre-lifecycle server still answers after stop")
	}
	if pidAlive(sf.PID) {
		t.Fatalf("pid %d survived the signal fallback", sf.PID)
	}
}

// TestRestartKeepsAddressAndSwitchesBuild is the upgrade flow end to end.
func TestRestartKeepsAddressAndSwitchesBuild(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)
	stopOnCleanup(t, home)

	out, code := runBin(t, home, "restart", "--json")
	if code != 0 {
		t.Fatalf("restart: %d %s", code, out)
	}
	var res struct {
		Running bool   `json:"running"`
		Addr    string `json:"addr"`
		PID     int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("restart json: %v\n%s", err, out)
	}
	if !res.Running || res.Addr != sf.Addr {
		t.Fatalf("restart changed the address: %+v (was %s)", res, sf.Addr)
	}
	if res.PID == sf.PID {
		t.Fatalf("restart reused pid %d", res.PID)
	}
	if pidAlive(sf.PID) {
		t.Fatalf("old pid %d is still running", sf.PID)
	}
	next, err := server.ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if next.PID != res.PID || next.Addr != sf.Addr || next.Token == "" {
		t.Fatalf("handshake not rewritten: %+v", next)
	}
	if out, code := runBin(t, home, "run", "--cwd", t.TempDir(), "hello"); code != 0 {
		t.Fatalf("restarted server does not accept work: %d %s", code, out)
	}
}

// TestServeReplaceRefusesSameBuild pins the degradation of --replace: swapping
// an identical binary would kill in-flight work for no benefit.
func TestServeReplaceRefusesSameBuild(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)

	out, code := runBin(t, home, "serve", "-d", "--replace")
	if code != 0 {
		t.Fatalf("serve --replace: %d %s", code, out)
	}
	next, err := server.ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if next.PID != sf.PID {
		t.Fatalf("--replace restarted an identical build: %d -> %d", sf.PID, next.PID)
	}
}

// TestServeReplaceSwapsMismatchedBuild covers swapping a different build, which
// is what a freshly installed binary sees.
func TestServeReplaceSwapsMismatchedBuild(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)
	stopOnCleanup(t, home)

	old := sf
	old.Build = "0.0.1-old"
	if err := server.WriteServerFile(home, old); err != nil {
		t.Fatal(err)
	}
	out, code := runBin(t, home, "serve", "-d", "--replace")
	if code != 0 {
		t.Fatalf("serve --replace: %d %s", code, out)
	}
	next, err := server.ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if next.PID == sf.PID {
		t.Fatal("--replace did not restart the daemon")
	}
	if next.Build == old.Build {
		t.Fatalf("new daemon still reports %q", next.Build)
	}
	if next.Addr != sf.Addr {
		t.Fatalf("replace changed the address: %s -> %s", sf.Addr, next.Addr)
	}
}

// TestStatusReportsOlderServerBuild keeps the advisory mismatch honest and
// checks it is a warning, not a failure.
func TestStatusReportsOlderServerBuild(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)

	old := sf
	old.Build = "0.0.1-old"
	if err := server.WriteServerFile(home, old); err != nil {
		t.Fatal(err)
	}
	out, code := runBin(t, home, "status")
	if code != 0 {
		t.Fatalf("status: %d %s", code, out)
	}
	if !strings.Contains(out, "0.0.1-old") {
		t.Fatalf("status did not report the older build:\n%s", out)
	}
}

// TestBareKIActivatesNewBuild covers the flow users actually take after
// installing a release: run `ki` again. It must replace a daemon from a
// different build, and keep its address (stop deletes server.json, so an
// implementation that reads the address afterwards would silently move the
// server to the configured default port).
func TestBareKIActivatesNewBuild(t *testing.T) {
	home, _ := isolate(t)
	sf := startServe(t, home)
	stopOnCleanup(t, home)
	sf.Build = "0.0.1-old"
	if err := server.WriteServerFile(home, sf); err != nil {
		t.Fatal(err)
	}

	out, code := runBin(t, home)
	if code != 0 {
		t.Fatalf("bare ki: %d %s", code, out)
	}
	if !strings.Contains(out, "replacing ki server") {
		t.Fatalf("bare ki did not report the replacement:\n%s", out)
	}
	next, err := server.ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if next.PID == sf.PID {
		t.Fatal("bare ki did not restart the daemon")
	}
	if next.Addr != sf.Addr {
		t.Fatalf("bare ki moved the server: %s -> %s", sf.Addr, next.Addr)
	}
	if next.Build == sf.Build {
		t.Fatalf("bare ki kept the old build %q", next.Build)
	}
	if !pingAddr(t, next.Addr) {
		t.Fatal("replacement server does not answer")
	}
}

// stopOnCleanup ends any daemon this test leaves behind. Replacement daemons
// started by restart/--replace/bare ki are detached, so startServe's cleanup
// does not own them; on Windows such a process keeps KI_HOME open and
// t.TempDir's RemoveAll then fails with a sharing violation. Registered after
// startServe, so it runs before that cleanup (cleanups are LIFO) and before the
// temp directory is removed.
func stopOnCleanup(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		// Why not runBin: it derives the command from t.Context(), which Go
		// cancels just before cleanups run, so the stop would fail with
		// "context canceled" and leave the daemon running.
		//nolint:gosec // the test-built binary with fixed arguments
		cmd := exec.CommandContext(context.Background(), builtKI(t), "stop", "--timeout", "5s")
		cmd.Env = childEnv(home)
		_, _ = cmd.CombinedOutput()
	})
}

func pingAddr(t *testing.T, addr string) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode == http.StatusOK
}
