package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ki/internal/buildinfo"
	"ki/internal/config"
	"ki/internal/server"
)

// defaultStopTimeout bounds how long `ki stop`/`ki restart` wait for a daemon
// to finish its owned cleanup before escalating to a signal. Cleanup closes
// extension sidecars, process managers and open runs, so a short wait avoids
// leaving a half-dead tree behind on every upgrade.
const defaultStopTimeout = 10 * time.Second

func newStopCommand() *cobra.Command {
	var timeout time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConfig("client", nil, func(cfg config.Config) error {
				return stopDaemon(cfg, timeout, asJSON, cmd.OutOrStdout())
			})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", defaultStopTimeout, "how long to wait for a graceful stop before escalating")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON result")
	return cmd
}

func newRestartCommand() *cobra.Command {
	var timeout time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the server from this binary",
		Long: "Replace the running server with one started from this binary, on the " +
			"address the previous instance recorded (or the configured one).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConfig("client", nil, func(cfg config.Config) error {
				return restartDaemon(cfg, timeout, asJSON, cmd.OutOrStdout())
			})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", defaultStopTimeout, "how long to wait for the old server to stop before escalating")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON result")
	return cmd
}

func newStatusCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the running server's address, pid, version and build",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConfig("client", nil, func(cfg config.Config) error {
				return printStatus(cfg, asJSON, cmd.OutOrStdout())
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON result")
	return cmd
}

// serverStatus is the projection `ki status` prints and --json exports.
type serverStatus struct {
	Running       bool      `json:"running"`
	Addr          string    `json:"addr,omitempty"`
	PID           int       `json:"pid,omitempty"`
	PIDAlive      bool      `json:"pidAlive,omitempty"`
	PIDSource     string    `json:"pidSource,omitempty"`
	Version       string    `json:"version,omitempty"`
	Exe           string    `json:"exe,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	Since         string    `json:"uptime,omitempty"`
	Home          string    `json:"home"`
	ClientVersion string    `json:"clientVersion"`
	ClientExe     string    `json:"clientExe,omitempty"`
	// Mismatch explains why this client's binary is not the one serving, so
	// `ki status` answers "did the upgrade take effect?" without a second look.
	Mismatch string `json:"mismatch,omitempty"`
	Stale    bool   `json:"stale,omitempty"`
}

func printStatus(cfg config.Config, asJSON bool, out io.Writer) error {
	st := inspectServer(cfg)
	if asJSON {
		return writeJSONLine(out, st)
	}
	if !st.Running {
		if st.Stale {
			fmt.Fprintf(out, "ki server is not running (stale server.json for %s)\n", st.Addr)
		} else {
			fmt.Fprintln(out, "ki server is not running")
		}
		fmt.Fprintf(out, "client\t%s\t%s\n", st.ClientVersion, st.ClientExe)
		fmt.Fprintf(out, "home\t%s\n", st.Home)
		return nil
	}
	fmt.Fprintf(out, "addr\t%s\n", st.Addr)
	fmt.Fprintf(out, "pid\t%d (%s)\n", st.PID, st.PIDSource)
	fmt.Fprintf(out, "version\t%s\n", orUnknown(st.Version))
	fmt.Fprintf(out, "exe\t%s\n", orUnknown(st.Exe))
	if !st.StartedAt.IsZero() {
		fmt.Fprintf(out, "started\t%s (%s ago)\n", st.StartedAt.Format(time.RFC3339), st.Since)
	}
	fmt.Fprintf(out, "client\t%s %s\n", st.ClientVersion, st.ClientExe)
	fmt.Fprintf(out, "home\t%s\n", st.Home)
	if st.Mismatch != "" {
		fmt.Fprintf(out, "note\t%s; run `ki restart`\n", st.Mismatch)
	}
	return nil
}

// inspectServer reads the handshake and asks the endpoint whether it is live.
// It never mutates anything: `ki status` is safe to run while a run is active.
func inspectServer(cfg config.Config) serverStatus {
	st := serverStatus{Home: cfg.Home, ClientVersion: buildinfo.Version, ClientExe: currentExe()}
	sf, err := server.ReadServerFile(cfg.Home)
	if err != nil {
		return st
	}
	st.Addr = sf.Addr
	st.Version = sf.Build
	st.Exe = sf.Exe
	st.StartedAt = sf.StartedAt
	st.PID = sf.PID
	if st.PID > 0 {
		st.PIDSource = "server.json"
		st.PIDAlive = processAlive(st.PID)
	}
	if sf.Addr == "" || !ping("http://"+sf.Addr, sf.Token) {
		st.Stale = sf.Addr != ""
		return st
	}
	st.Running = true
	if st.PID <= 0 {
		// A daemon from before the lifecycle commands left no pid. Its listener
		// still identifies it, which is what makes the first upgrade over an old
		// server possible without a manual kill.
		if pid := listenerPID(sf.Addr); pid > 0 {
			st.PID = pid
			st.PIDSource = "listener"
			st.PIDAlive = processAlive(pid)
		}
	}
	if !sf.StartedAt.IsZero() {
		st.Since = time.Since(sf.StartedAt).Round(time.Second).String()
	}
	st.Mismatch = buildMismatch(sf)
	return st
}

// buildMismatch names the difference between the live daemon and this binary.
// An empty result means the daemon already runs this build.
func buildMismatch(sf server.File) string {
	if sf.Build == "" && sf.Exe == "" {
		return "the running server predates build identity reporting"
	}
	if sf.Build != buildinfo.Version {
		return fmt.Sprintf("server runs %s, this binary is %s", orUnknown(sf.Build), buildinfo.Version)
	}
	if sf.Exe != "" && !sameFilePath(sf.Exe, currentExe()) {
		return fmt.Sprintf("server runs %s, this binary is %s", sf.Exe, currentExe())
	}
	return ""
}

func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func sameFilePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	a, b = resolve(a), resolve(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// stopDaemon ends the daemon described by the handshake and waits for it.
//
// Stop is idempotent: with no handshake, or with one that no longer answers,
// it reports that nothing is running and cleans up the stale file. The graceful
// path (POST /v1/shutdown) runs owned cleanup; the signal path exists for
// daemons built before that route, and is the migration step for an upgrade.
func stopDaemon(cfg config.Config, timeout time.Duration, asJSON bool, out io.Writer) error {
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}
	sf, err := server.ReadServerFile(cfg.Home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return reportStop(out, asJSON, map[string]any{"running": false, "stopped": false, "reason": "no server.json"})
		}
		return err
	}
	if sf.Addr == "" || !ping("http://"+sf.Addr, sf.Token) {
		if err := server.RemoveServerFile(cfg.Home, sf.Token); err != nil {
			return err
		}
		return reportStop(out, asJSON, map[string]any{
			"running": false, "stopped": false, "addr": sf.Addr, "reason": "stale server.json removed",
		})
	}

	pid := sf.PID
	if pid <= 0 {
		pid = listenerPID(sf.Addr)
	}

	// Graceful first. A 404 means the daemon predates the route, which is the
	// expected first step when upgrading from a build without lifecycle
	// commands; the signal fallback below covers it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	gracefulErr := doJSONContext(ctx, "http://"+sf.Addr, sf.Token, http.MethodPost, "/v1/shutdown", nil, nil)
	cancel()
	graceful := gracefulErr == nil

	if !waitStopped(sf, pid, timeout) {
		if pid <= 0 {
			return errHint(fmt.Errorf("ki server at %s did not stop", sf.Addr),
				"the running build predates `ki stop` and did not record its pid; stop it once by hand and retry")
		}
		// SIGTERM lets a daemon that predates /v1/shutdown still reap its
		// children through the ordinary OS teardown.
		_ = terminateProcess(pid)
		wait := timeout
		if wait > 5*time.Second {
			wait = 5 * time.Second
		}
		if !waitStopped(sf, pid, wait) {
			_ = forceKillProcess(pid)
			if !waitStopped(sf, pid, 3*time.Second) {
				return fmt.Errorf("ki server at %s (pid %d) did not exit", sf.Addr, pid)
			}
		}
	}
	if err := server.RemoveServerFile(cfg.Home, sf.Token); err != nil {
		return err
	}
	return reportStop(out, asJSON, map[string]any{
		"running":  false,
		"stopped":  true,
		"addr":     sf.Addr,
		"pid":      pid,
		"graceful": graceful,
	})
}

func reportStop(out io.Writer, asJSON bool, v map[string]any) error {
	if asJSON {
		return writeJSONLine(out, v)
	}
	if stopped, _ := v["stopped"].(bool); stopped {
		fmt.Fprintf(out, "stopped ki server %v (pid %v)\n", v["addr"], v["pid"])
		return nil
	}
	if reason, _ := v["reason"].(string); reason != "" {
		fmt.Fprintf(out, "ki server is not running (%s)\n", reason)
	} else {
		fmt.Fprintln(out, "ki server is not running")
	}
	return nil
}

// waitStopped reports whether the daemon stopped answering and, when its pid is
// known, whether the process actually exited. Checking only health would call a
// daemon "stopped" while its cleanup still holds ports and child processes.
func waitStopped(sf server.File, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		serving := ping("http://"+sf.Addr, sf.Token)
		alive := pid > 0 && processAlive(pid)
		if !serving && !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// restartDaemon replaces the daemon with one started from this binary.
func restartDaemon(cfg config.Config, timeout time.Duration, asJSON bool, out io.Writer) error {
	addr := daemonAddr(cfg)
	if err := stopDaemon(cfg, timeout, false, io.Discard); err != nil {
		return err
	}
	sf, pid, started, err := startDetached(cfg, addr)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONLine(out, map[string]any{
			"running": true, "addr": sf.Addr, "pid": pid, "started": started, "version": buildinfo.Version,
		})
	}
	if started {
		fmt.Fprintf(out, "ki server %s pid %d version %s\n", sf.Addr, pid, buildinfo.Version)
	} else {
		fmt.Fprintf(out, "ki server %s already running\n", sf.Addr)
	}
	return nil
}
