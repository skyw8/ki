//go:build !windows

package extensionbuild

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
