// Package extensionbuild installs a bundled extension's native executable from
// its source package when the executable is missing. Builds run at the package
// root, use the current platform, preserve dependency locks and compiler caches,
// and publish through an atomic rename. A package-local lock prevents concurrent
// first launches from racing a build. Go packages require only Go; the Rust search
// exception additionally requires its documented native build prerequisites.
package extensionbuild
