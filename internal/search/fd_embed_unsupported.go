//go:build (!linux && !darwin && !windows) || (linux && !amd64 && !arm64) || (darwin && !amd64 && !arm64) || (windows && !amd64 && !arm64)

package search

func embeddedFD() ([]byte, string) { return nil, "" }
