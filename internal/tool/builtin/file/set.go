package filetools

import (
	toolapi "ki/internal/tool"
)

// Set binds file tools to one session workspace and mutation queue.
type Set struct {
	CWD       string
	ReadOps   ReadOperations
	Mutations *MutationQueue
}

func (s Set) Build(richRead, applyPatch bool) []toolapi.Tool {
	if s.Mutations == nil {
		s.Mutations = NewMutationQueue()
	}
	out := []toolapi.Tool{readTool{cwd: s.CWD, rich: richRead, ops: s.ReadOps}}
	if applyPatch {
		out = append(out, applyPatchTool{cwd: s.CWD, mutations: s.Mutations})
	} else {
		out = append(out, writeTool{cwd: s.CWD, mutations: s.Mutations}, editTool{cwd: s.CWD, mutations: s.Mutations})
	}
	return append(out, grepTool{cwd: s.CWD}, globTool{cwd: s.CWD})
}
