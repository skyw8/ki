package llmprotocol

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{{"3", now.Add(3 * time.Second)}, {"0", now}, {"-1", time.Time{}}, {"invalid", time.Time{}}, {now.Add(time.Minute).Format(http.TimeFormat), now.Add(time.Minute)}, {"9223372036854775807", time.Time{}}} {
		if got := ParseRetryAfter(tc.value, now); !got.Equal(tc.want) {
			t.Fatalf("%q: %v want %v", tc.value, got, tc.want)
		}
	}
}
