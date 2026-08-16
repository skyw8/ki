//go:build !windows

package server

import (
	"os"
	"strings"
)

func isHiddenName(name string, _ os.FileInfo) bool {
	return strings.HasPrefix(name, ".")
}
