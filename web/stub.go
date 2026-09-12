//go:build !embed

package web

import "embed"

// Dist is empty when ki is compiled without -tags embed.
//
// Why: web/dist is build output and is not tracked by git, so an unconditional
// //go:embed all:dist would fail to compile on a fresh checkout. The stub keeps
// CLI/API builds working; serveUI reports the UI as not built.
var Dist embed.FS

// HasAssets reports whether the SPA was embedded, i.e. ki was built with
// -tags embed.
func HasAssets() bool { return false }
