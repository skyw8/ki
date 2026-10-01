package main

import "golang.org/x/sys/unix"

func osVersion() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(name.Release[:])
}
