// Package main builds the bundled extensions into binary or standalone source packages.
// Binary packages need no compiler; source packages retain a missing-binary
// installer and a minimal local Ki module so they build outside the checkout.
// Output paths are resolved through symlinks before package replacement; an
// output cannot contain the repository or overlap bundled extension sources.
package main
