//go:build embed

package web

import "embed"

// Dist is the Vite-built production frontend (web/dist).
//
//go:embed all:dist
var Dist embed.FS

// HasAssets reports whether the SPA was embedded, i.e. ki was built with
// -tags embed.
func HasAssets() bool { return true }
