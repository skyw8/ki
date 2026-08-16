// Package workspace is the durable workspace registry.
//
// One record is a stable id over a host directory. Session membership is
// cwd equality after Abs/EvalSymlinks. The file is {KI_HOME}/workspaces.json.
// Cascade delete of the directory and session logs is the server's job.
// Temporary workspaces live under {KI_HOME}/workspace/tmp+<timestamp>.
// Contract: docs/workspace.md.
package workspace
