package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ki/internal/config"
	"ki/internal/klog"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/server"
)

// Main is the process entrypoint.
func Main(args []string) int {
	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := klog.Setup(cfg.Home, cfg.Log.Level); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(args) > 0 && args[0] == "serve" {
		return runServe(cfg, args[1:])
	}
	flags, rest := parseFlags(args)
	if flags.Detach && flags.Prompt == "" && len(rest) == 0 {
		return runDetach(cfg, flags)
	}
	if flags.Detach {
		if code := runDetach(cfg, flags); code != 0 {
			return code
		}
	}
	return runClient(cfg, flags, strings.Join(rest, " "))
}

type flags struct {
	Detach  bool
	Session string
	Model   string
	CWD     string
	Addr    string
	Prompt  string
	Compact bool
	Fork    bool
}

func parseFlags(args []string) (flags, []string) {
	var f flags
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" || a == "--detach":
			f.Detach = true
		case a == "--session" && i+1 < len(args):
			i++
			f.Session = args[i]
		case strings.HasPrefix(a, "--session="):
			f.Session = strings.TrimPrefix(a, "--session=")
		case a == "--model" && i+1 < len(args):
			i++
			f.Model = args[i]
		case strings.HasPrefix(a, "--model="):
			f.Model = strings.TrimPrefix(a, "--model=")
		case a == "--cwd" && i+1 < len(args):
			i++
			f.CWD = args[i]
		case a == "--addr" && i+1 < len(args):
			i++
			f.Addr = args[i]
		case a == "compact":
			f.Compact = true
		case a == "fork":
			f.Fork = true
		case strings.HasPrefix(a, "-"):
			// ignore unknown
		default:
			rest = append(rest, a)
		}
	}
	return f, rest
}

func runServe(cfg config.Config, args []string) int {
	addr := cfg.Server.Addr
	for i := 0; i < len(args); i++ {
		if args[i] == "--addr" && i+1 < len(args) {
			addr = args[i+1]
		}
	}
	st := streamer(cfg)
	srv := server.New(server.Options{Config: cfg, Streamer: st})
	if err := srv.ListenAndServe(addr); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runDetach(cfg config.Config, f flags) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	addr := f.Addr
	if addr == "" {
		addr = cfg.Server.Addr
	}
	cmd := exec.Command(self, "serve", "--addr", addr)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// wait until server.json exists
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sf, err := server.ReadServerFile(cfg.Home); err == nil && sf.Addr != "" {
			fmt.Fprintf(os.Stderr, "ki server %s pid %d\n", sf.Addr, cmd.Process.Pid)
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "server did not come up")
	return 1
}

func streamer(cfg config.Config) loop.Streamer {
	if os.Getenv("KI_FAKE") == "1" {
		return &provider.Scripted{}
	}
	return nil // Server uses liveFromConfig
}

func runClient(cfg config.Config, f flags, prompt string) int {
	base, token, stop, err := ensureServer(cfg, f)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if stop != nil {
		defer stop()
	}
	id := f.Session
	if id == "" {
		var created map[string]any
		if err := doJSON(base, token, "POST", "/v1/sessions", map[string]any{"cwd": f.CWD, "model": f.Model}, &created); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		id, _ = created["id"].(string)
	} else if f.Model != "" {
		// model writeback happens on prompt
	}
	if f.Fork {
		var out map[string]any
		if err := doJSON(base, token, "POST", "/v1/sessions/"+id+"/fork", nil, &out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(out["id"])
		return 0
	}
	if f.Compact {
		var out map[string]any
		if err := doJSON(base, token, "POST", "/v1/sessions/"+id+"/compact", nil, &out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("compacted", out["id"])
		return 0
	}
	if strings.TrimSpace(prompt) == "" {
		fmt.Fprintf(os.Stderr, "session %s\n", id)
		return 0
	}
	ctx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSig()
	go func() {
		<-ctx.Done()
		_ = doJSON(base, token, "POST", "/v1/sessions/"+id+"/abort", nil, nil)
	}()
	if err := doJSON(base, token, "POST", "/v1/sessions/"+id+"/prompt", map[string]any{"text": prompt, "model": f.Model}, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := streamEvents(ctx, base, token, id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "\nsession_id: %s\n", id)
	return 0
}

func ensureServer(cfg config.Config, f flags) (base, token string, stop func(), err error) {
	if sf, e := server.ReadServerFile(cfg.Home); e == nil && sf.Addr != "" {
		if ping("http://"+sf.Addr, sf.Token) {
			return "http://" + sf.Addr, sf.Token, nil, nil
		}
	}
	// in-process
	srv := server.New(server.Options{Config: cfg, Streamer: streamer(cfg)})
	addr := "127.0.0.1:0"
	if f.Addr != "" {
		addr = f.Addr
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(addr) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a := srv.Addr(); a != "" {
			return "http://" + a, srv.Token(), func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}, nil
		}
		select {
		case e := <-errCh:
			return "", "", nil, e
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	return "", "", nil, fmt.Errorf("in-process server failed to bind")
}

func ping(base, token string) bool {
	req, _ := http.NewRequest("GET", base+"/v1/health", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func doJSON(base, token, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, string(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func streamEvents(ctx context.Context, base, token, id string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev loop.Event
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
			continue
		}
		printEvent(ev)
		if ev.Type == loop.AgentEnd {
			return nil
		}
	}
	return sc.Err()
}

func printEvent(ev loop.Event) {
	switch ev.Type {
	case loop.MessageUpdate:
		if ev.AssistantMessageEvent != nil && ev.AssistantMessageEvent.Delta != "" {
			fmt.Fprint(os.Stdout, ev.AssistantMessageEvent.Delta)
		}
	case loop.ToolExecutionStart:
		fmt.Fprintf(os.Stdout, "\n[%s %s]\n", ev.ToolName, ev.ToolCallID)
	case loop.ToolExecutionEnd:
		fmt.Fprintf(os.Stdout, "[%s done err=%v]\n", ev.ToolName, ev.IsError)
	case loop.MessageEnd:
		if ev.Message != nil && ev.Message.Role == "assistant" && ev.AssistantMessageEvent == nil {
			// final text already streamed via updates
		}
	}
}
