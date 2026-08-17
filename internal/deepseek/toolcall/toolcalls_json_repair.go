package toolcall

import (
	"regexp"
	"strings"
)

func repairInvalidJSONBackslashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s) + 10)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			if i+1 < len(runes) {
				next := runes[i+1]
				switch next {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					out.WriteRune('\\')
					out.WriteRune(next)
					i++
					continue
				case 'u':
					if i+5 < len(runes) {
						isHex := true
						for j := 1; j <= 4; j++ {
							r := runes[i+1+j]
							if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
								isHex = false
								break
							}
						}
						if isHex {
							out.WriteRune('\\')
							out.WriteRune('u')
							for j := 1; j <= 4; j++ {
								out.WriteRune(runes[i+1+j])
							}
							i += 5
							continue
						}
					}
				}
			}
			// Not a valid escape sequence, double it
			out.WriteString("\\\\")
		} else {
			out.WriteRune(runes[i])
		}
	}
	return out.String()
}

var unquotedKeyPattern = regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)

// missingArrayBracketsPattern identifies a sequence of two or more JSON objects separated by commas
// that immediately follow a colon, which indicates a missing array bracket `[` `]`.
// E.g., "key": {"a": 1}, {"b": 2} -> "key": [{"a": 1}, {"b": 2}]
// NOTE: The pattern uses (?:[^{}]|\{[^{}]*\})* to support single-level nested {} objects,
// which handles cases like {"content": "x", "input": {"q": "y"}}
var missingArrayBracketsPattern = regexp.MustCompile(`(:\s*)(\{(?:[^{}]|\{[^{}]*\})*\}(?:\s*,\s*\{(?:[^{}]|\{[^{}]*\})*\})+)`)

func RepairLooseJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// 1. Replace unquoted keys: {key: -> {"key":
	s = unquotedKeyPattern.ReplaceAllString(s, `$1"$2":`)

	// 2. Heuristic: Fix missing array brackets for list of objects
	// e.g., : {obj1}, {obj2} -> : [{obj1}, {obj2}]
	// This specifically addresses DeepSeek's "list hallucination"
	s = missingArrayBracketsPattern.ReplaceAllString(s, `$1[$2]`)

	return s
}
