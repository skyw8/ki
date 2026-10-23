//go:build !linux && !darwin

package cli

// exitedProcess cannot be detected without procfs; the liveness probe falls
// back to signal 0, which on macOS reports an exited-but-unreaped child as
// gone (the kernel reaps it), so no extra check is needed there.
func exitedProcess(int) bool { return false }
