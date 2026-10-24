//go:build !embed

package web

import "embed"

var Dist embed.FS

func HasAssets() bool { return false }
