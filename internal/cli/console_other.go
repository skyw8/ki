//go:build !windows

package cli

// Why: off Windows a file manager starts the binary with an inherited terminal
// (Terminal.app, or the launcher behind a .desktop entry), so a bare `ki`
// already reaches the WebUI launcher and needs no console handling. These
// helpers only exist so Main stays platform-neutral.

func launchedFromShell() bool { return false }

func hideOwnConsole() {}

func showFatalBox(string, string) {}
