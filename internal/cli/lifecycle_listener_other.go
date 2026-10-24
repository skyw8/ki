//go:build !linux && !darwin && !windows

package cli

func listenerPID(string) int { return 0 }
