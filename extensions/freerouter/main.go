package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
)

var logOutput io.Writer = os.Stderr

func command() *cobra.Command {
	var listen, baseURL string
	var verbose bool
	cmd := &cobra.Command{Use: "freerouter", Short: "OpenRouter free-model race router", SilenceUsage: true, SilenceErrors: true}
	cmd.PersistentFlags().StringVar(&listen, "listen", "", "HTTP listen address (default 127.0.0.1:18427)")
	cmd.PersistentFlags().StringVar(&baseURL, "base-url", "", "OpenRouter API base URL")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose logging")
	run := func(cmd *cobra.Command, _ []string) error {
		return runProgram(cmd.Context(), cmd.Name() == "sidecar", listen, baseURL)
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.RunE = run
	cmd.Args = cobra.NoArgs
	for _, name := range []string{"serve", "sidecar"} {
		child := &cobra.Command{Use: name, Args: cobra.NoArgs, RunE: run}
		if name == "serve" {
			child.Short = "Run standalone HTTP proxy only"
		} else {
			child.Short = "Run as ki extension sidecar (HTTP + NDJSON JSON-RPC on stdin/stdout)"
		}
		cmd.AddCommand(child)
	}
	return cmd
}
func runProgram(ctx context.Context, sidecar bool, listen, baseURL string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	root := ""
	c := loadStandalone(listen, baseURL)
	if sidecar {
		root = extensionRootFromEnv()
		c = loadSidecar(root)
	} else if c.APIKey == "" {
		return fmt.Errorf("OPENROUTER_API_KEY (or FREEROUTER_API_KEY) is required for standalone mode")
	}
	p := newPool(ctx, c, &http.Client{})
	server, listener, err := startHTTP(p, c.Listen)
	if err != nil {
		return err
	}
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Serve(listener) }()
	defer func() {
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	if sidecar {
		rpcDone := make(chan error, 1)
		go func() { rpcDone <- runSidecar(ctx, p, root, os.Stdin, os.Stdout) }()
		select {
		case err := <-rpcDone:
			return err
		case err := <-httpDone:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-httpDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := command().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[freerouter] %v\n", err)
		os.Exit(1)
	}
}
