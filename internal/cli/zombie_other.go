//go:build !linux && !darwin

package cli

func exitedProcess(int) bool { return false }
