package e2e

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/server"
)

func TestServeReuseAndAuth(t *testing.T) {
	home, proj := isolate(t)
	sf := startServe(t, home)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+sf.Addr+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health %d", res.StatusCode)
	}

	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+sf.Addr+"/v1/sessions/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	out, code := runBin(t, home, "run", "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("client: %d %s", code, out)
	}
	id := mustSessionID(t, out, "")
	sf2, err := server.ReadServerFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if sf2.Addr != sf.Addr {
		t.Fatalf("client started a new server: %s vs %s", sf2.Addr, sf.Addr)
	}
	_ = id
}

func TestDetachLeavesServer(t *testing.T) {
	home, proj := isolate(t)
	//nolint:gosec // this subprocess is intentionally the e2e system under test
	cmd := exec.CommandContext(t.Context(), builtKI(t), "serve", "-d", "--addr", "127.0.0.1:0")
	cmd.Env = childEnv(home)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detach: %v\n%s", err, b)
	}
	text := string(b)
	registerDetachedCleanup(t, text)
	if !strings.Contains(text, "ki server") {
		t.Fatalf("detach output: %s", text)
	}

	deadline := time.Now().Add(3 * time.Second)
	var sf server.File
	for time.Now().Before(deadline) {
		sf, err = server.ReadServerFile(home)
		if err == nil && sf.Addr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sf.Addr == "" {
		t.Fatal("detach did not write server.json")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+sf.Addr+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health after detach: %d", res.StatusCode)
	}

	out, code := runBin(t, home, "run", "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("client after detach: %d %s", code, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("fake text: %s", out)
	}
}

func TestDetachReportsStartupFailure(t *testing.T) {
	home, _ := isolate(t)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	out, code := runBin(t, home, "serve", "-d", "--addr", ln.Addr().String())
	if code == 0 {
		t.Fatalf("detach onto an occupied port must fail, output: %s", out)
	}
	for _, want := range []string{"server did not come up", "another process owns", "hint: run `ki serve"} {
		if !strings.Contains(out, want) {
			t.Fatalf("startup failure output missing %q:\n%s", want, out)
		}
	}
}

func TestDetachedServerSurvivesLauncherExit(t *testing.T) {
	home, proj := isolate(t)
	out, code := runBin(t, home, "serve", "-d", "--addr", "127.0.0.1:0")
	if code != 0 {
		t.Fatalf("detach: %d %s", code, out)
	}
	registerDetachedCleanup(t, out)
	var sf server.File
	var readErr error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sf, readErr = server.ReadServerFile(home); readErr == nil && sf.Addr != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sf.Addr == "" {
		t.Fatalf("server.json not written: %v\n%s", readErr, out)
	}

	runBin(t, home, "reload")
	time.Sleep(200 * time.Millisecond)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+sf.Addr+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("daemon died after the launcher exited: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health after reload: %d", res.StatusCode)
	}

	_, clientCode := runBin(t, home, "run", "--cwd", proj, "hello")
	if clientCode != 0 {
		t.Fatalf("prompt after launcher exit: %d", clientCode)
	}
}

func TestParallelSessionsOnOneServer(t *testing.T) {
	home, proj := isolate(t)
	_ = startServe(t, home)

	outA, code := runBin(t, home, "run", "--cwd", proj, "alpha")
	if code != 0 {
		t.Fatalf("create A: %d %s", code, outA)
	}
	outB, code := runBin(t, home, "run", "--cwd", proj, "beta")
	if code != 0 {
		t.Fatalf("create B: %d %s", code, outB)
	}
	idA := mustSessionID(t, outA, "")
	idB := mustSessionID(t, outB, "")
	if idA == idB {
		t.Fatal("expected two sessions")
	}

	var (
		wg     sync.WaitGroup
		out1   string
		out2   string
		c1, c2 int
	)
	wg.Go(func() {
		out1, c1 = runBin(t, home, "run", "--session", idA, "alpha")
	})
	wg.Go(func() {
		out2, c2 = runBin(t, home, "run", "--session", idB, "beta")
	})
	wg.Wait()
	if c1 != 0 || c2 != 0 {
		t.Fatalf("parallel exits %d %d\nA:%s\nB:%s", c1, c2, out1, out2)
	}
	if !strings.Contains(readJSONL(t, sessionDir(t, home, idA)), "alpha") {
		t.Fatal("session A missing prompt")
	}
	if !strings.Contains(readJSONL(t, sessionDir(t, home, idB)), "beta") {
		t.Fatal("session B missing prompt")
	}
}

func detachPID(s string) int {
	const mark = " pid "
	i := strings.LastIndex(s, mark)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(s[i+len(mark):])
	rest = strings.Fields(rest)[0]
	n, _ := strconv.Atoi(rest)
	return n
}

func registerDetachedCleanup(t *testing.T, output string) {
	t.Helper()
	pid := detachPID(output)
	if pid <= 0 {
		t.Fatalf("detached server output missing pid: %s", output)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find detached server %d: %v", pid, err)
	}
	t.Cleanup(func() {
		if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill detached server %d: %v", pid, err)
			return
		}
		if err := waitDetachedProcess(p); err != nil {
			t.Errorf("wait for detached server %d to exit: %v", pid, err)
		}
	})
}
