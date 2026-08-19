package toolcall

import (
	"encoding/json"
	"html"
	"strings"
)

func parseLooseJSONArrayValue(raw, paramName string) ([]any, bool) {
	if preservesCDATAStringParameter(paramName) {
		return nil, false
	}
	trimmed := strings.TrimSpace(html.UnescapeString(raw))
	if trimmed == "" {
		return nil, false
	}

	if parsed, ok := parseLooseJSONArrayCandidate(trimmed, paramName); ok {
		return parsed, true
	}

	segments, ok := splitTopLevelJSONValues(trimmed)
	if !ok {
		return nil, false
	}

	out := make([]any, 0, len(segments))
	for _, segment := range segments {
		parsed, ok := parseLooseArrayElementValue(segment)
		if !ok {
			return nil, false
		}
		out = append(out, parsed)
	}
	return out, true
}

func parseLooseJSONArrayCandidate(raw, paramName string) ([]any, bool) {
	parsed, ok := parseLooseArrayElementValue(raw)
	if !ok {
		return nil, false
	}
	return coerceArrayValue(parsed, paramName)
}

func parseLooseArrayElementValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(html.UnescapeString(raw))
	if trimmed == "" {
		return nil, false
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed, true
	}

	repairedBackslashes := repairInvalidJSONBackslashes(trimmed)
	if repairedBackslashes != trimmed {
		if err := json.Unmarshal([]byte(repairedBackslashes), &parsed); err == nil {
			return parsed, true
		}
	}

	repairedLoose := RepairLooseJSON(trimmed)
	if repairedLoose != trimmed {
		if err := json.Unmarshal([]byte(repairedLoose), &parsed); err == nil {
			return parsed, true
		}
	}

	if strings.Contains(trimmed, "<") && strings.Contains(trimmed, ">") {
		if parsedXML, ok := parseXMLFragmentValue(trimmed); ok {
			return parsedXML, true
		}
	}

	return nil, false
}

func coerceArrayValue(value any, paramName string) ([]any, bool) {
	switch x := value.(type) {
	case []any:
		return x, true
	case map[string]any:
		if len(x) != 1 {
			return nil, false
		}

		if items, ok := x["item"]; ok {
			if arr, ok := coerceArrayValue(items, ""); ok {
				return arr, true
			}
			return []any{items}, true
		}

		if paramName != "" {
			if wrapped, ok := x[paramName]; ok {
				if arr, ok := coerceArrayValue(wrapped, ""); ok {
					return arr, true
				}
			}
		}
	}
	return nil, false
}

func splitTopLevelJSONValues(raw string) ([]string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}

	values := make([]string, 0, 2)
	start := 0
	depth := 0
	inString := false
	escaped := false

	for i, r := range trimmed {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				segment := strings.TrimSpace(trimmed[start:i])
				if segment == "" {
					return nil, false
				}
				values = append(values, segment)
				start = i + 1
			}
		}
	}

	last := strings.TrimSpace(trimmed[start:])
	if last == "" {
		return nil, false
	}
	values = append(values, last)
	if len(values) < 2 {
		return nil, false
	}
	return values, true
}
