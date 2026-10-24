package process

import "unicode/utf8"

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
