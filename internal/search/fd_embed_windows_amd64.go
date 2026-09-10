//go:build windows && amd64

package search

import _ "embed"

//go:embed assets/fd-windows-amd64.exe
var fdWindowsAMD64 []byte

func embeddedFD() ([]byte, string) { return fdWindowsAMD64, "fd.exe" }
