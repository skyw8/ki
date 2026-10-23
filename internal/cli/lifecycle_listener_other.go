//go:build !linux && !darwin && !windows

package cli

// listenerPID is unavailable off the supported release targets. Callers treat 0
// as "the daemon's pid is unknown" and fall back to the signal path.
func listenerPID(string) int { return 0 }
