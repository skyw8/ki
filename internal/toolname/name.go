package toolname

import (
	"fmt"
	"strings"
)

// Canonical splits ASCII CamelCase words, including acronym boundaries.
func Canonical(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty tool name")
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return "", fmt.Errorf("invalid tool name %q", name)
		}
		if i == 0 && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", fmt.Errorf("invalid tool name %q", name)
		}
		if c >= 'A' && c <= 'Z' {
			if i > 0 && name[i-1] != '_' && ((name[i-1] >= 'a' && name[i-1] <= 'z') || (name[i-1] >= '0' && name[i-1] <= '9') || (i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z')) {
				b.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// Pascal returns the single advertised case alias for a canonical name.
func Pascal(name string) string {
	var b strings.Builder
	upper := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
		upper = false
	}
	return b.String()
}

func MustCanonical(name string) string {
	n, err := Canonical(name)
	if err != nil {
		panic(err)
	}
	return n
}

// Aliases includes the native extension spelling without making matching case-insensitive.
func Aliases(name string) ([]string, error) {
	c, err := Canonical(name)
	if err != nil {
		return nil, err
	}
	out := []string{c}
	for _, n := range []string{Pascal(c), name} {
		found := false
		for _, v := range out {
			if v == n {
				found = true
			}
		}
		if !found {
			out = append(out, n)
		}
	}
	return out, nil
}

// Equal compares registered spellings, not arbitrary case variants.
func Equal(request, registered string) bool {
	aliases, err := Aliases(registered)
	if err != nil {
		return false
	}
	for _, alias := range aliases {
		if request == alias {
			return true
		}
	}
	return false
}

var Builtins = []string{"read", "write", "edit", "grep", "glob", "apply_patch", "exec_command", "write_stdin", "spawn_agent", "send_message", "followup_task", "wait_agent", "interrupt_agent", "list_agents"}

func IsBuiltin(name string) bool {
	for _, n := range Builtins {
		if Equal(name, n) {
			return true
		}
	}
	return false
}
