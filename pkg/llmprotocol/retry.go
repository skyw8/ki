package llmprotocol

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type retryAdviceError struct {
	err   error
	after time.Time
}

func (e *retryAdviceError) Error() string         { return e.err.Error() }
func (e *retryAdviceError) Unwrap() error         { return e.err }
func (e *retryAdviceError) RetryAfter() time.Time { return e.after }

func ParseRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Time{}
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil {
		return date
	}
	return time.Time{}
}

func withRetryAdvice(err error, header http.Header) error {
	if after := ParseRetryAfter(header.Get("Retry-After"), time.Now()); !after.IsZero() {
		return &retryAdviceError{err: err, after: after}
	}
	return err
}
