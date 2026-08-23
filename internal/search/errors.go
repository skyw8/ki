package search

import "errors"

var (
	errPatternRequired   = errors.New("pattern is required")
	errPathNotExist      = errors.New("path does not exist")
	errPathNotDirectory  = errors.New("path is not a directory")
	errEmptyRGPath       = errors.New("ripgrep returned an empty path")
	errRipgrep           = errors.New("ripgrep")
	errInvalidRGMatch    = errors.New("ripgrep returned an invalid match event")
	errEmbeddedRGMissing = errors.New("embedded ripgrep is unavailable for this platform")
)
