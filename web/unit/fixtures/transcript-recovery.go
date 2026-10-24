//go:build ignore

package main

import (
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("go", "test", "./internal/session", "-run", "^TestTranscriptConformance$", "-count=1")
	cmd.Env = append(os.Environ(), "KI_UPDATE_TRANSCRIPT_FIXTURES=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}
