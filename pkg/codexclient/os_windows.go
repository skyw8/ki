package codexclient

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func hostOS() (string, string) {
	v := windows.RtlGetVersion()
	return "Windows", fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
