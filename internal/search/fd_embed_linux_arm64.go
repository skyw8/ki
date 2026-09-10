//go:build linux && arm64

package search

import _ "embed"

//go:embed assets/fd-linux-arm64
var fdLinuxARM64 []byte

func embeddedFD() ([]byte, string) { return fdLinuxARM64, "fd" }
