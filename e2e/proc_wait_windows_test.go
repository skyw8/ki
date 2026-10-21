//go:build windows

package e2e

import "os"

func waitDetachedProcess(p *os.Process) error {
	// Why: the server keeps its session files open until process exit, and
	// Windows does not allow TempDir cleanup while those handles remain open.
	_, err := p.Wait()
	return err
}
