// Package processenv centralizes the environment passed to Ki child processes.
//
// Proxy variables are inherited from the Ki process rather than discovered or
// hardcoded. This keeps HTTP clients and every descendant process on the same
// system proxy configuration.
package processenv
