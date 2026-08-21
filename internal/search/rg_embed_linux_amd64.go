//go:build linux && amd64

package search

import _ "embed"

//go:embed assets/rg-linux-amd64
var rgLinuxAMD64 []byte

func embeddedRG() ([]byte, string) { return rgLinuxAMD64, "rg" }
