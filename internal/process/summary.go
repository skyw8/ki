package process

import "unicode/utf8"

func validSummaryHead(data []byte, limit int) []byte {
	data = data[:min(len(data), limit)]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return data
}

func (p *shellProcess) summaryLocked(budget int) (string, int64) {
	if p.total <= int64(budget) {
		if p.total <= int64(len(p.head)) {
			return string(p.head[:int(p.total)]), 0
		}
		tail := p.summaryTail.bytes(0, 0)
		overlap := len(p.head) + len(tail) - int(p.total)
		return string(p.head) + string(tail[max(overlap, 0):]), 0
	}
	marker := "\n…\n"
	if budget < len(marker)+2 {
		return "", p.total
	}
	payloadBudget := budget - len(marker)
	head := validSummaryHead(p.head, payloadBudget/2)
	tailBudget := payloadBudget - len(head)
	tail := p.summaryTail.bytes(max(0, p.summaryTail.size-tailBudget), tailBudget)
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	tail = validSummaryHead(tail, len(tail))
	return string(head) + marker + string(tail), p.total - int64(len(head)+len(tail))
}
