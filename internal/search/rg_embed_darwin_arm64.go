//go:build darwin && arm64

package search

import _ "embed"

//go:embed assets/rg-darwin-arm64
var rgDarwinARM64 []byte

func embeddedRG() ([]byte, string) { return rgDarwinARM64, "rg" }
