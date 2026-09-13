//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cmdJobs maps a started command to the job object that owns its process tree.
var cmdJobs sync.Map

func detachCmd(cmd *exec.Cmd) {
	// New process group so the serve console's Ctrl+C is not the only
	// cancellation path; killCmd reaps the tree itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// afterStart joins a running command to a kill-on-close job object. Every
// process the command spawns afterwards inherits the membership, so terminating
// the job reaps descendants even when their recorded parent pid says otherwise.
func afterStart(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	if previous, loaded := cmdJobs.Swap(cmd, job); loaded {
		if old, ok := previous.(windows.Handle); ok {
			_ = windows.CloseHandle(old)
		}
	}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// The job owns the tree: a launcher that does not record parent pids the way
	// the process snapshot expects is still terminated here.
	if entry, ok := cmdJobs.LoadAndDelete(cmd); ok {
		if job, ok := entry.(windows.Handle); ok {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}
	// Why: Process.Kill is TerminateProcess and leaves descendants behind, and
	// `taskkill /T` walks the parent chain recorded by the process launcher,
	// which misses children of an MSYS shell. Walk the toolhelp snapshot instead
	// and terminate deepest first, repeating while descendants remain: the
	// launcher can spawn another child between the snapshot and the kill, and a
	// surviving child keeps the stdout pipe open, which hangs Wait.
	for attempt := 0; attempt < 3; attempt++ {
		descendants := descendantPIDs(uint32(cmd.Process.Pid))
		if len(descendants) == 0 {
			break
		}
		for _, pid := range descendants {
			if proc, err := os.FindProcess(int(pid)); err == nil {
				_ = proc.Kill()
			}
		}
		time.Sleep(20 * time.Millisecond)
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
