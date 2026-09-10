//go:build darwin && arm64

package search

import _ "embed"

//go:embed assets/fd-darwin-arm64
var fdDarwinARM64 []byte

func embeddedFD() ([]byte, string) { return fdDarwinARM64, "fd" }
