package cli

import "golang.org/x/sys/unix"

// zombleState is the P_stat value Darwin reports for an exited, unreaped
// process. x/sys/unix does not export it on darwin, and it is stable ABI.
const zombleState = 5 // SZOMB

// exitedProcess reports whether pid has already terminated but is still
// visible as a zombie because its parent has not reaped it.
//
// Why sysctl instead of kill(pid, 0): a zombie still answers signal 0 on
// Darwin, so a daemon that finished shutdown would look alive and `ki stop`
// would wait out its whole timeout.
func exitedProcess(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return false
	}
	return info.Proc.P_stat == zombleState
}
