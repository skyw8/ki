//go:build windows

package cli

import (
	"syscall"
	"unsafe"

	"github.com/inconshreveable/mousetrap"
)

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
