//go:build windows

package cli

import (
	"syscall"
	"unsafe"

	"github.com/inconshreveable/mousetrap"
)

// Why: Windows attaches a fresh console window to a console-subsystem binary
// that Explorer double-clicks, and that window disappears the moment the
// process exits. The bare `ki` command is the WebUI launcher, so the window is
// hidden and a failed launch is reported in a message box: stderr text would
// vanish with the console before anyone could read it.
var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procGetConsole  = user32.NewProc("GetConsoleWindow")
	procShowWindow  = user32.NewProc("ShowWindow")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	swHide          = 0
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

// launchedFromShell reports whether Explorer started the process, i.e. the user
// double-clicked ki.exe rather than running it from a terminal.
func launchedFromShell() bool { return mousetrap.StartedByExplorer() }

func hideOwnConsole() {
	hwnd, _, _ := procGetConsole.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}

func showFatalBox(title, text string) {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError|mbSetForeground)
}
