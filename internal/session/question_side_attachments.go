package session

import (
	"encoding/json"
	"path/filepath"

	"ki/internal/types"
)

func CopyMessageAttachments(sourceDir, targetDir string, message types.Message) (types.Message, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return types.Message{}, err
	}
	var copied types.Message
	if err := json.Unmarshal(raw, &copied); err != nil {
		return types.Message{}, err
	}
	entries := []Entry{{Type: "message", Message: &copied}}
	refs := referencedAttachments(entries, sourceDir)
	rewriteEntryAttachmentPaths(&entries[0], sourceDir, targetDir)
	if len(refs) > 0 {
		if err := copyAttachmentRefs(filepath.Join(sourceDir, "attachments"), filepath.Join(targetDir, "attachments"), refs); err != nil {
			return types.Message{}, err
		}
	}
	return *entries[0].Message, nil
}
