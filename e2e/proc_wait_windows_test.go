//go:build windows

package e2e

import "os"

func waitDetachedProcess(p *os.Process) error {
	_, err := p.Wait()
	return err
}
