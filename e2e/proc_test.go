package e2e

import (
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

	res, err := http.Get("http://" + sf.Addr + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("health %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET", "http://"+sf.Addr+"/v1/sessions/x", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	out, code := runBin(t, home, "--cwd", proj, "hello")
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
	cmd := exec.Command(builtKI(t), "-d", "--addr", "127.0.0.1:0")
	cmd.Env = childEnv(home)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detach: %v\n%s", err, b)
	}
	text := string(b)
	if !strings.Contains(text, "ki server") {
		t.Fatalf("detach output: %s", text)
	}
	pid := detachPID(text)
	if pid > 0 {
		t.Cleanup(func() {
			p, err := os.FindProcess(pid)
			if err == nil {
				_ = p.Kill()
			}
		})
	} else {
		t.Cleanup(func() {
			if sf, err := server.ReadServerFile(home); err == nil && sf.Addr != "" {
				_, _ = http.Get("http://" + sf.Addr + "/v1/health")
			}
		})
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
	res, err := http.Get("http://" + sf.Addr + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("health after detach: %d", res.StatusCode)
	}

	out, code := runBin(t, home, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("client after detach: %d %s", code, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("fake text: %s", out)
	}
}

func TestParallelSessionsOnOneServer(t *testing.T) {
	home, proj := isolate(t)
	_ = startServe(t, home)

	outA, code := runBin(t, home, "--cwd", proj)
	if code != 0 {
		t.Fatalf("create A: %d %s", code, outA)
	}
	outB, code := runBin(t, home, "--cwd", proj)
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
	wg.Add(2)
	go func() {
		defer wg.Done()
		out1, c1 = runBin(t, home, "--session", idA, "alpha")
	}()
	go func() {
		defer wg.Done()
		out2, c2 = runBin(t, home, "--session", idB, "beta")
	}()
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
	// "ki server 127.0.0.1:1234 pid 99"
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
