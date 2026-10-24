//go:build !windows

package cli

func launchedFromShell() bool { return false }

func hideOwnConsole() {}

func showFatalBox(string, string) {}
