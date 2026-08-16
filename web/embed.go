// Package web embeds the Vite-built SPA served by ki serve on non-/v1 paths.
package web

import "embed"

// Dist is the production frontend (web/dist). Rebuild with npm run build.
//
//go:embed all:dist
var Dist embed.FS
