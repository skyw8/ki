package tools

import (
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	"ki/internal/loop"
)

const applyPatchPreviewInterval = 500 * time.Millisecond

type applyPatchArgumentConsumer struct {
	parser   *streamingPatchParser
	lastSent time.Time
	pending  []applyPatchChangeDetail
}

func (applyPatchTool) NewArgumentDiffConsumer() loop.ToolArgumentDiffConsumer {
	return &applyPatchArgumentConsumer{parser: newStreamingPatchParser()}
}

func (c *applyPatchArgumentConsumer) Consume(delta string) (any, bool) {
	hunks, err := c.parser.Push(delta)
	if err != nil || len(hunks) == 0 {
		return nil, false
	}
	// This snapshot is syntax-only and must never be confused with the
	// committed delta produced after filesystem verification and execution.
	changes := previewPatchChanges(hunks)
	now := time.Now()
	if !c.lastSent.IsZero() && now.Sub(c.lastSent) < applyPatchPreviewInterval {
		c.pending = changes
		return nil, false
	}
	c.lastSent = now
	c.pending = nil
	return map[string]any{"changes": changes}, true
}

func (c *applyPatchArgumentConsumer) Finish() (any, bool) {
	if _, err := c.parser.Finish(); err != nil {
		return nil, false
	}
	if c.pending == nil {
		return nil, false
	}
	changes := c.pending
	c.pending = nil
	return map[string]any{"changes": changes}, true
}

func previewPatchChanges(hunks []patchHunk) []applyPatchChangeDetail {
	out := make([]applyPatchChangeDetail, 0, len(hunks))
	for _, h := range hunks {
		change := applyPatchChangeDetail{Path: h.path, Kind: patchKindName(h.kind), MovePath: h.movePath}
		switch h.kind {
		case patchAdd:
			change.UnifiedDiff, _ = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{B: difflib.SplitLines(h.content), FromFile: h.path + " (new)", ToFile: h.path, Context: 1})
		case patchUpdate:
			var b strings.Builder
			for _, chunk := range h.chunks {
				if chunk.hasCtx {
					b.WriteString("@@ ")
					b.WriteString(chunk.context)
					b.WriteByte('\n')
				} else {
					b.WriteString("@@\n")
				}
				for _, line := range chunk.old {
					b.WriteByte('-')
					b.WriteString(line)
					b.WriteByte('\n')
				}
				for _, line := range chunk.new {
					b.WriteByte('+')
					b.WriteString(line)
					b.WriteByte('\n')
				}
				if chunk.eof {
					b.WriteString(patchEOFMarker)
					b.WriteByte('\n')
				}
			}
			change.UnifiedDiff = b.String()
		}
		out = append(out, change)
	}
	return out
}
