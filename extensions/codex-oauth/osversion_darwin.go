package main

import "golang.org/x/sys/unix"

func osVersion() string {
	if version, err := unix.Sysctl("kern.osproductversion"); err == nil && version != "" {
		return version
	}
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(name.Release[:])
}
