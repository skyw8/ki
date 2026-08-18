//go:build !windows

package provider

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
