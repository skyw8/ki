//go:build windows

package server

import (
	"os"
	"strings"
	"syscall"
)

func isHiddenName(name string, fi os.FileInfo) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	st, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok || st == nil {
		return false
	}
	return st.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
