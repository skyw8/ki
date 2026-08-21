//go:build darwin && amd64

package search

import _ "embed"

//go:embed assets/rg-darwin-amd64
var rgDarwinAMD64 []byte

func embeddedRG() ([]byte, string) { return rgDarwinAMD64, "rg" }
