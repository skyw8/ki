package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"ki/internal/config"
	"ki/internal/logging"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/server"
)

// Main is the process entrypoint.
func Main(args []string) (exitCode int) {
	defer func() {
		if logging.Recover("process panic") {
			exitCode = 1
		}
	}()

	cmd := newRootCommand()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "ki",
		Short:         "An extensible agent runtime",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withConfig("client", nil, runDefault)
		},
	}
	root.Version = Version
	root.SetVersionTemplate("ki {{.Version}}\n")
	root.AddCommand(newServeCommand(), newRunCommand(), newConfigCommand(), newSessionCommand(), newReloadCommand(), newVersionCommand())
	return root
}

func withConfig(role string, settings *viper.Viper, fn func(config.Config) error) error {
	cwd, _ := os.Getwd()
	cfg, err := config.LoadWithViper(cwd, settings)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logCloser, err := logging.Setup(logging.Options{
		Home:       cfg.Home,
		Level:      cfg.Log.Level,
		Role:       role,
		MaxSizeMB:  cfg.Log.MaxSizeMB,
		MaxBackups: cfg.Log.MaxBackups,
	})
	if err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	defer func() { _ = logCloser.Close() }()
	return fn(cfg)
}

func newServeCommand() *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server and embedded WebUI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings := viper.New()
			if cmd.Flags().Changed("addr") {
				if err := settings.BindPFlag("server.addr", cmd.Flags().Lookup("addr")); err != nil {
					return fmt.Errorf("bind server address flag: %w", err)
				}
			}
			return withConfig("server", settings, func(cfg config.Config) error {
				if detach {
					return runDetach(cfg, flags{Addr: cfg.Server.Addr})
				}
				return runServe(cfg, cfg.Server.Addr)
			})
		},
	}
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "run the server in the background")
	cmd.Flags().String("addr", "", "listen address")
	return cmd
}

func newRunCommand() *cobra.Command {
	var f flags
	cmd := &cobra.Command{
		Use:   "run [flags] <prompt>",
		Short: "Send a prompt and stream the agent run",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return withConfig("client", nil, func(cfg config.Config) error {
				return runClient(cfg, f, strings.Join(args, " "))
			})
		},
	}
	cmd.Flags().StringVar(&f.Session, "session", "", "resume an existing session")
	cmd.Flags().StringVar(&f.Model, "model", "", "model or provider/model to use")
	cmd.Flags().StringVar(&f.CWD, "cwd", "", "working directory for a new session")
	cmd.Flags().StringVar(&f.Addr, "addr", "", "server listen address when starting one")
	cmd.Flags().BoolVar(&f.Steer, "steer", false, "insert into the current run when the session is busy")
	cmd.Flags().BoolVar(&f.Queue, "queue", false, "queue until the current run finishes when the session is busy")
	return cmd
}

func newReloadCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload session resources and MCP connections on the running server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withConfig("client", nil, runReload)
		},
	}
}

func runReload(cfg config.Config) error {
	sf, err := server.ReadServerFile(cfg.Home)
	if err != nil || sf.Addr == "" || !ping("http://"+sf.Addr, sf.Token) {
		return errNoLiveServer
	}
	var out map[string]any
	if err := doJSON("http://"+sf.Addr, sf.Token, "/v1/reload", nil, &out); err != nil {
		return err
	}
	fmt.Println("reloaded")
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), Version)
		},
	}
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect Ki configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the configuration file locations",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withConfig("client", nil, func(cfg config.Config) error {
				cwd, _ := os.Getwd()
				_, _ = fmt.Fprintf(os.Stdout, "home: %s\n", cfg.Home)
				_, _ = fmt.Fprintf(os.Stdout, "global: %s\n", filepath.Join(cfg.Home, "ki.toml"))
				_, _ = fmt.Fprintf(os.Stdout, "project: %s\n", filepath.Join(cwd, ".ki", "ki.toml"))
				return nil
			})
		},
	})
	return cmd
}

func newSessionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage sessions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	for _, action := range []string{"compact", "fork"} {
		var id string
		sub := &cobra.Command{
			Use:   action,
			Short: action + " a session",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				if id == "" {
					return errSessionRequired
				}
				return withConfig("client", nil, func(cfg config.Config) error {
					return runSessionAction(cfg, id, action)
				})
			},
		}
		sub.Flags().StringVar(&id, "session", "", "session id")
		cmd.AddCommand(sub)
	}
	return cmd
}

type flags struct {
	Session string
	Model   string
	CWD     string
	Addr    string
	Steer   bool
	Queue   bool
}

var (
	errSessionRequired     = errors.New("--session is required")
	errServerDidNotComeUp  = errors.New("server did not come up")
	errPromptRequired      = errors.New("prompt is required")
	errSteerQueueExclusive = errors.New("--steer and --queue are mutually exclusive")
	errInProcessServerBind = errors.New("in-process server failed to bind")
	errHTTPResponse        = errors.New("HTTP request failed")
	errNoLiveServer        = errors.New("ki server is not running")
)

func runServe(cfg config.Config, addr string) error {
	st := streamer(cfg)
	srv, err := server.New(server.Options{Config: cfg, Streamer: st})
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	if err := srv.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "err", err)
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func runDetach(cfg config.Config, f flags) error {
	sf, pid, started, err := startDetached(cfg, f.Addr)
	if err != nil {
		return err
	}
	if started {
		fmt.Fprintf(os.Stderr, "ki server %s pid %d\n", sf.Addr, pid)
	} else {
		fmt.Fprintf(os.Stderr, "ki server %s already running\n", sf.Addr)
	}
	return nil
}

func runDefault(cfg config.Config) error {
	sf, pid, started, err := startDetached(cfg, cfg.Server.Addr)
	if err != nil {
		return err
	}
	url := browserURL(sf.Addr)
	if started {
		fmt.Fprintf(os.Stderr, "ki server %s pid %d\n", sf.Addr, pid)
	}
	fmt.Fprintf(os.Stderr, "ki web %s\n", url)
	if err := openBrowser(url); err != nil {
		// Browser launch is deliberately best-effort: headless shells and SSH
		// sessions are valid Ki clients even when no GUI opener is available.
		fmt.Fprintf(os.Stderr, "could not open browser: %v\n", err)
	}
	return nil
}

func startDetached(cfg config.Config, addr string) (sf server.File, pid int, started bool, err error) {
	if existing, e := server.ReadServerFile(cfg.Home); e == nil && existing.Addr != "" && ping("http://"+existing.Addr, existing.Token) {
		return existing, 0, false, nil
	}
	oldToken := ""
	if existing, e := server.ReadServerFile(cfg.Home); e == nil {
		oldToken = existing.Token
	}
	self, err := os.Executable()
	if err != nil {
		return server.File{}, 0, false, err
	}
	if addr == "" {
		addr = cfg.Server.Addr
	}
	//nolint:gosec // self is the current executable resolved by os.Executable.
	cmd := exec.CommandContext(context.Background(), self, "serve", "--addr", addr)
	cmd.Env = os.Environ()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return server.File{}, 0, false, fmt.Errorf("start detached server: %w", err)
	}
	// Wait for both the child to publish a fresh server file and the endpoint
	// to answer. Checking only file existence lets a stale server.json report a
	// failed child as healthy.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if current, e := server.ReadServerFile(cfg.Home); e == nil && current.Addr != "" && current.Token != oldToken && ping("http://"+current.Addr, current.Token) {
			return current, cmd.Process.Pid, true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return server.File{}, 0, false, errServerDidNotComeUp
}

func streamer(_ config.Config) loop.Streamer {
	if os.Getenv("KI_FAKE") == "1" {
		return &provider.Scripted{}
	}
	return nil // Server builds the live router from the provider registry.
}

func runClient(cfg config.Config, f flags, prompt string) error {
	base, token, stop, err := ensureServer(cfg, f)
	if err != nil {
		return err
	}
	if stop != nil {
		defer stop()
	}
	id := f.Session
	if id == "" {
		var created map[string]any
		if err := doJSON(base, token, "/v1/sessions", map[string]any{"cwd": f.CWD, "model": f.Model}, &created); err != nil {
			return err
		}
		id, _ = created["id"].(string)
	}
	if strings.TrimSpace(prompt) == "" {
		return errPromptRequired
	}
	ctx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSig()
	go func() {
		<-ctx.Done()
		// Why Background: ctx is already canceled (that's why this goroutine
		// woke). Reusing it would cancel the abort HTTP request before serve
		// sees it, so Ctrl+C would never reach POST /abort.
		// WithoutCancel keeps the abort request alive after the signal context
		// is canceled while retaining any values attached to the client context.
		abortCtx, abortCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer abortCancel()
		_ = doJSONContext(abortCtx, base, token, "POST", "/v1/sessions/"+id+"/abort", nil, nil)
	}()
	if f.Steer && f.Queue {
		return errSteerQueueExclusive
	}
	body := map[string]any{"text": prompt, "model": f.Model}
	if f.Steer {
		body["delivery"] = "steer"
	}
	if f.Queue {
		body["delivery"] = "queue"
	}
	if err := doJSON(base, token, "/v1/sessions/"+id+"/prompt", body, nil); err != nil {
		return err
	}
	if err := streamEvents(ctx, base, token, id); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "\nsession_id: %s\n", id)
	return nil
}

func runSessionAction(cfg config.Config, id, action string) error {
	base, token, stop, err := ensureServer(cfg, flags{})
	if err != nil {
		return err
	}
	if stop != nil {
		defer stop()
	}
	var out map[string]any
	if err := doJSON(base, token, "/v1/sessions/"+id+"/"+action, nil, &out); err != nil {
		return err
	}
	if action == "compact" {
		fmt.Println("compacted", out["id"])
	} else {
		fmt.Println(out["id"])
	}
	return nil
}

func ensureServer(cfg config.Config, f flags) (base, token string, stop func(), err error) {
	if sf, e := server.ReadServerFile(cfg.Home); e == nil && sf.Addr != "" {
		if ping("http://"+sf.Addr, sf.Token) {
			return "http://" + sf.Addr, sf.Token, nil, nil
		}
	}
	// in-process
	srv, err := server.New(server.Options{Config: cfg, Streamer: streamer(cfg)})
	if err != nil {
		return "", "", nil, fmt.Errorf("create in-process server: %w", err)
	}
	addr := "127.0.0.1:0"
	if f.Addr != "" {
		addr = f.Addr
	}
	errCh := make(chan error, 1)
	go func() {
		defer logging.Recover("in-process server panic")
		errCh <- srv.ListenAndServe(addr)
	}()
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
	return "", "", nil, errInProcessServerBind
}

func ping(base, _ string) bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func doJSON(base, token, path string, body any, out any) error {
	return doJSONContext(context.Background(), base, token, http.MethodPost, path, body, out)
}

func doJSONContext(ctx context.Context, base, token, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%w: %s %s: %d %s", errHTTPResponse, method, path, res.StatusCode, string(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func streamEvents(ctx context.Context, base, token, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
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
			_, _ = fmt.Fprint(os.Stdout, ev.AssistantMessageEvent.Delta)
		}
	case loop.ToolExecutionStart:
		_, _ = fmt.Fprintf(os.Stdout, "\n[%s %s]\n", ev.ToolName, ev.ToolCallID)
	case loop.ToolExecutionEnd:
		_, _ = fmt.Fprintf(os.Stdout, "[%s done err=%v]\n", ev.ToolName, ev.IsError)
	case loop.AgentStart, loop.AgentEnd, loop.TurnStart, loop.TurnEnd,
		loop.RequestHeader, loop.MessageStart, loop.MessageEnd,
		loop.ToolExecutionUpdate, loop.PatchApplyUpdated, loop.CompactionStart, loop.CompactionEnd,
		loop.ContextUsage, loop.MCPServerFailed, loop.MCPToolsChanged:
		return
	}
}
