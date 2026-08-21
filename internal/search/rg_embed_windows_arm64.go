//go:build windows && arm64

package search

import _ "embed"

//go:embed assets/rg-windows-arm64.exe
var rgWindowsARM64 []byte

func embeddedRG() ([]byte, string) { return rgWindowsARM64, "rg.exe" }
