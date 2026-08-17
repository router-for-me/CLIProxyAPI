package toolcall

import (
	"encoding/json"
	"html"
	"strings"
	"unicode"
)

func parseToolCallInput(v any) map[string]any {
	switch x := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return x
	case string:
		raw := strings.TrimSpace(html.UnescapeString(x))
		if raw == "" {
			return map[string]any{}
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
			repairPathLikeControlChars(parsed)
			return parsed
		}
		// Try to repair invalid backslashes (common in Windows paths output by models)
		repaired := repairInvalidJSONBackslashes(raw)
		if repaired != raw {
			if err := json.Unmarshal([]byte(repaired), &parsed); err == nil && parsed != nil {
				repairPathLikeControlChars(parsed)
				return parsed
			}
		}
		// Try to repair loose JSON in string argument as well
		repairedLoose := RepairLooseJSON(raw)
		if repairedLoose != raw {
			if err := json.Unmarshal([]byte(repairedLoose), &parsed); err == nil && parsed != nil {
				repairPathLikeControlChars(parsed)
				return parsed
			}
		}
		return map[string]any{"_raw": raw}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return map[string]any{}
		}
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err == nil && parsed != nil {
			return parsed
		}
		return map[string]any{}
	}
}

func repairPathLikeControlChars(m map[string]any) {
	for k, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			repairPathLikeControlChars(vv)
		case []any:
			for _, item := range vv {
				if child, ok := item.(map[string]any); ok {
					repairPathLikeControlChars(child)
				}
			}
		case string:
			if isPathLikeKey(k) && containsControlRune(vv) {
				m[k] = escapeControlRunes(vv)
			}
		}
	}
}

func isPathLikeKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(k, "path") || strings.Contains(k, "file")
}

func containsControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func escapeControlRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
