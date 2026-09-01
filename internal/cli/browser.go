package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"

	"ki/internal/processenv"
)

var errBrowserUnavailable = errors.New("browser opener unavailable")

func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%w: %s", errBrowserUnavailable, name)
	}
	//nolint:gosec // path is the OS browser opener selected by LookPath.
	cmd := exec.CommandContext(context.Background(), path, args...)
	cmd.Env = processenv.WithProxyEnvironment(processenv.ChildEnvironment())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start browser opener: %w", err)
	}
	return nil
}
