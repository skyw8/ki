//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func detachCmd(cmd *exec.Cmd) {
	// New process group so the serve console's Ctrl+C is not the only
	// cancellation path; killCmd reaps the tree itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Why: Process.Kill is TerminateProcess and leaves descendants behind, and
	// `taskkill /T` walks the parent chain recorded by the process launcher,
	// which misses children of an MSYS shell. Walk the toolhelp snapshot instead
	// and terminate deepest first, so a bash pipeline (find, sleep, ...) cannot
	// keep the stdout pipe open and hang Wait.
	for _, pid := range descendantPIDs(uint32(cmd.Process.Pid)) {
		if proc, err := os.FindProcess(int(pid)); err == nil {
			_ = proc.Kill()
		}
	}
	// Belt and braces for launchers whose children are not linked through the
	// toolhelp parent field, then the launcher itself.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}

// descendantPIDs returns every process under root, children before parents.
func descendantPIDs(root uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	children := map[uint32][]uint32{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	var out []uint32
	var walk func(uint32)
	walk = func(parent uint32) {
		for _, child := range children[parent] {
			walk(child)
			out = append(out, child)
		}
	}
	walk(root)
	return out
}
