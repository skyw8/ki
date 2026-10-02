package process

import "unicode/utf8"

// outputRing retains a UTF-8 suffix without copying the whole retention window
// on every write from a noisy process. The backing allocation is strictly capped.
type outputRing struct {
	data []byte
	head int
	size int
}

func (r *outputRing) append(chunk []byte, limit int) int {
	if len(chunk) == 0 {
		return 0
	}
	needed := min(limit, r.size+len(chunk))
	if needed > len(r.data) {
		// Most commands print only a few lines. Grow lazily rather than reserving
		// the full 1 MiB window for every retained completed terminal.
		data := make([]byte, min(limit, max(1024, max(needed, 2*len(r.data)))))
		copy(data, r.bytes(0, 0))
		r.data, r.head = data, 0
	}
	dropped := 0
	if len(chunk) >= limit {
		dropped = r.size + len(chunk) - limit
		copy(r.data, chunk[len(chunk)-limit:])
		r.head, r.size = 0, limit
	} else {
		if excess := r.size + len(chunk) - limit; excess > 0 {
			dropped = excess
			r.head = (r.head + excess) % len(r.data)
			r.size -= excess
		}
		tail := (r.head + r.size) % len(r.data)
		n := copy(r.data[tail:], chunk)
		copy(r.data, chunk[n:])
		r.size += len(chunk)
	}
	// Both callers append complete runes, but the byte cap can split the first.
	for r.size > 0 && !utf8.RuneStart(r.data[r.head]) {
		r.head = (r.head + 1) % len(r.data)
		r.size--
		dropped++
	}
	return dropped
}

func (r *outputRing) bytes(offset, limit int) []byte {
	if offset >= r.size {
		return nil
	}
	n := r.size - offset
	if limit > 0 && n > limit {
		n = limit
	}
	start := (r.head + offset) % len(r.data)
	if start+n <= len(r.data) {
		return r.data[start : start+n]
	}
	out := make([]byte, n)
	copied := copy(out, r.data[start:])
	copy(out[copied:], r.data[:n-copied])
	return out
}

func (r *outputRing) reset() { r.head, r.size = 0, 0 }
