package thinking

// Split recognizes only canonical standard-level Fast selections. Unknown
// values remain unchanged so callers can reject them rather than normalize typos.
func Split(level string) (base string, fast bool) {
	switch level {
	case "off fast":
		return "off", true
	case "minimal fast":
		return "minimal", true
	case "low fast":
		return "low", true
	case "medium fast":
		return "medium", true
	case "high fast":
		return "high", true
	case "xhigh fast":
		return "xhigh", true
	case "max fast":
		return "max", true
	default:
		return level, false
	}
}
