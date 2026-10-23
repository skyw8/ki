// Package buildinfo carries the identity of this build.
//
// Release builds override both values through -ldflags
// (`-X ki/internal/buildinfo.Version=1.2.3 -X ki/internal/buildinfo.ReleaseTag=v1.2.3`),
// so an installed binary reports and installs from the release it came from.
// The package exists so both the CLI and the extension installer can read these
// values without importing each other.
package buildinfo

// Version is the semantic version of this build.
var Version = "0.4.0"

// ReleaseTag is the release the extension installer downloads from. It is empty
// for a local build, which then follows the newest published release instead of
// guessing a tag that may not exist.
var ReleaseTag = ""
