package idgen

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewV7 returns a 32-character, hyphen-free UUIDv7.
func NewV7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// EntryID returns a 32-character, hyphen-free UUIDv7. Entry IDs are full UUIDs
// rather than truncated values so their collision resistance is not reduced
// to the 32-bit space of the old 8-hex format.
func EntryID() (string, error) {
	return NewV7()
}

// FileTimestamp formats t for directory names.
func FileTimestamp(t time.Time) string {
	// Go recognizes fractional seconds only when they follow a dot; format
	// with the dot first, then replace it so the result remains filename-safe.
	s := t.UTC().Format("2006-01-02T15-04-05.000000000Z")
	return strings.Replace(s, ".", "-", 1)
}
