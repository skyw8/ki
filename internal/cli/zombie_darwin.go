package cli

import "golang.org/x/sys/unix"

const zombleState = 5

func exitedProcess(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return false
	}
	return info.Proc.P_stat == zombleState
}
