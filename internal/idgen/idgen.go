package idgen

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	last uint64
)

// NewV7 returns a 32-char hex UUIDv7.
func NewV7() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	mu.Lock()
	if ms <= last {
		ms = last + 1
	}
	last = ms
	mu.Unlock()
	binary.BigEndian.PutUint64(b[0:8], ms<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// EntryID returns an 8-hex id not present in used.
func EntryID(used map[string]struct{}) string {
	var b [4]byte
	for i := 0; i < 100; i++ {
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		if _, ok := used[id]; !ok {
			return id
		}
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())[:8]
}

// FileTimestamp formats t for directory names (ISO with : and . as -).
func FileTimestamp(t time.Time) string {
	s := t.UTC().Format("2006-01-02T15-04-05-000000000Z")
	return s
}
