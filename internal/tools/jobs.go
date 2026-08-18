package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// JobStore holds background bash jobs.
type JobStore struct {
	mu   sync.Mutex
	jobs map[string]*bgJob
}

type bgJob struct {
	mu   sync.Mutex
	path string
	buf  bytes.Buffer
}

func (j *bgJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n, err := j.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer job output: %w", err)
	}
	return n, nil
}

// NewJobStore creates an empty store.
func NewJobStore() *JobStore { return &JobStore{jobs: map[string]*bgJob{}} }

// Start launches command in the background and returns id + output path.
func (s *JobStore) Start(ctx context.Context, cwd, command string) (id, path string) {
	id = fmt.Sprintf("bg-%d", time.Now().UnixNano())
	f, err := os.CreateTemp("", "ki-bg-*.log")
	if err != nil {
		return id, ""
	}
	path = f.Name()
	job := &bgJob{path: path}
	s.mu.Lock()
	s.jobs[id] = job
	s.jobs[path] = job
	s.mu.Unlock()
	jobCtx := context.WithoutCancel(ctx)
	go func() {
		//nolint:gosec // executing the explicit Bash tool command is the feature contract.
		cmd := exec.CommandContext(jobCtx, "bash", "-lc", command)
		if cwd != "" {
			cmd.Dir = cwd
		}
		cmd.Stdout = io.MultiWriter(f, job)
		cmd.Stderr = cmd.Stdout
		_ = cmd.Run()
		_ = f.Close()
	}()
	return id, path
}

// Output returns captured output for a job id or path.
func (s *JobStore) Output(key string) (string, bool) {
	s.mu.Lock()
	j, ok := s.jobs[key]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	b, _ := os.ReadFile(j.path)
	if len(b) > 0 {
		return string(b), true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String(), true
}
