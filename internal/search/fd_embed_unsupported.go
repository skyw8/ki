//go:build (!linux && !darwin && !windows) || (linux && !amd64) || (darwin && !arm64) || (windows && !amd64)

package search

func embeddedFD() ([]byte, string) { return nil, "" }
