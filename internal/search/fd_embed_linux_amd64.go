//go:build linux && amd64

package search

import _ "embed"

//go:embed assets/fd-linux-amd64
var fdLinuxAMD64 []byte

func embeddedFD() ([]byte, string) { return fdLinuxAMD64, "fd" }
