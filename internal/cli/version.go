package cli

import "ki/internal/buildinfo"

// Version is the semantic version of this source tree. Release builds override
// it with the pushed tag through -ldflags (`-X ki/internal/cli.Version=1.2.3`),
// so an installed binary reports the release it came from while every local
// build reports the version the checkout is based on instead of a placeholder.
//
// The value lives in internal/buildinfo so packages that cannot import the CLI
// (the extension installer) read the same version.
var Version = buildinfo.Version
