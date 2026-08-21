//go:build windows && amd64

package search

import _ "embed"

//go:embed assets/rg-windows-amd64.exe
var rgWindowsAMD64 []byte

func embeddedRG() ([]byte, string) { return rgWindowsAMD64, "rg.exe" }
