package idgen

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func NewV7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func EntryID() (string, error) {
	return NewV7()
}

func FileTimestamp(t time.Time) string {
	s := t.UTC().Format("2006-01-02T15-04-05.000000000Z")
	return strings.Replace(s, ".", "-", 1)
}
