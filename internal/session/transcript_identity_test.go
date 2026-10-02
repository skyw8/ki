package session

import (
	"encoding/json"
	"testing"

	"ki/internal/types"
)

func TestTranscriptMetadataRetainsAcceptedIdentity(t *testing.T) {
	entry := Entry{Type: "message", ID: "notice", Message: &types.Message{
		Role: "user", Origin: "agent:/root/child", ClientRequestID: "accepted-notice",
		Content: []types.Content{{Type: "text", Text: "notification"}},
	}}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _, err := decodeMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Message == nil || metadata.Message.ClientRequestID != entry.Message.ClientRequestID ||
		metadata.Message.Origin != entry.Message.Origin {
		t.Fatalf("metadata lost accepted identity: %+v", metadata.Message)
	}
}
