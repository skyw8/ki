//go:build darwin && amd64

package search

import _ "embed"

//go:embed assets/fd-darwin-amd64
var fdDarwinAMD64 []byte

func embeddedFD() ([]byte, string) { return fdDarwinAMD64, "fd" }
