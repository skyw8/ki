//go:build linux && arm64

package search

import _ "embed"

//go:embed assets/rg-linux-arm64
var rgLinuxARM64 []byte

func embeddedRG() ([]byte, string) { return rgLinuxARM64, "rg" }
