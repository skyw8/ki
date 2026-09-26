//go:build !unix

package tooloutput

// processAlive cannot check another process on this platform, so the sweep
// falls back to the recorded heartbeat and the TTL.
func processAlive(int) (alive bool, known bool) {
	return false, false
}
