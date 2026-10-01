package main

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func osVersion() string {
	version := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber)
}
