package command

import (
	"regexp"
	"strconv"
	"strings"

	"ki/internal/resources"
)

var placeholderRe = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

func templateByName(snapshot resources.Snapshot, name string) (resources.PromptTemplate, bool) {
	for _, t := range snapshot.Prompts {
		if t.Name == name {
			return t, true
		}
	}
	return resources.PromptTemplate{}, false
}

// ExpandTemplate substitutes $1 / $@ style args. Unknown name returns false.
func ExpandTemplate(snapshot resources.Snapshot, name, args string) (string, bool) {
	t, ok := templateByName(snapshot, name)
	if !ok {
		return "", false
	}
	return substituteArgs(t.Body, parseCommandArgs(args)), true
}

func parseCommandArgs(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func substituteArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	return placeholderRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := placeholderRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		if sub[1] != "" {
			val := all
			if sub[1] != "@" && sub[1] != "ARGUMENTS" {
				i, _ := strconv.Atoi(sub[1])
				if i >= 1 && i <= len(args) {
					val = args[i-1]
				} else {
					val = ""
				}
			}
			if val != "" {
				return val
			}
			return sub[2]
		}
		if sub[3] != "" {
			start, _ := strconv.Atoi(sub[3])
			if start < 1 {
				start = 1
			}
			from := min(start-1, len(args))
			if sub[4] != "" {
				n, _ := strconv.Atoi(sub[4])
				end := min(from+n, len(args))
				return strings.Join(args[from:end], " ")
			}
			return strings.Join(args[from:], " ")
		}
		switch sub[5] {
		case "ARGUMENTS", "@":
			return all
		default:
			i, _ := strconv.Atoi(sub[5])
			if i >= 1 && i <= len(args) {
				return args[i-1]
			}
			return ""
		}
	})
}
