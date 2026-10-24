//go:build windows

package process

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var cmdJobs sync.Map

func detachCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

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
	if entry, ok := cmdJobs.LoadAndDelete(cmd); ok {
		if job, ok := entry.(windows.Handle); ok {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		descendants := descendantPIDs(uint32(cmd.Process.Pid))
		if len(descendants) == 0 {
			break
		}
		for _, pid := range descendants {
			if proc, err := os.FindProcess(int(pid)); err == nil {
				_ = proc.Kill()
				_ = proc.Release()
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	taskkill := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	taskkill.WaitDelay = ProcessWaitDelay
	_ = taskkill.Run()
	_ = cmd.Process.Kill()
}

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
	seen := map[uint32]bool{root: true}
	var walk func(uint32)
	walk = func(parent uint32) {
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			walk(child)
			out = append(out, child)
		}
	}
	walk(root)
	return out
}

func releaseCmd(cmd *exec.Cmd) {
	if entry, ok := cmdJobs.LoadAndDelete(cmd); ok {
		if job, ok := entry.(windows.Handle); ok {
			_ = windows.CloseHandle(job)
		}
	}
}
