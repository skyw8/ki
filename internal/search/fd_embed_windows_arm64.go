//go:build windows && arm64

package search

import _ "embed"

//go:embed assets/fd-windows-arm64.exe
var fdWindowsARM64 []byte

func embeddedFD() ([]byte, string) { return fdWindowsARM64, "fd.exe" }
