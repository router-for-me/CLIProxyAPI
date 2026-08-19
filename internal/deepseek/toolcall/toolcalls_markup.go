package toolcall

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

var toolCallMarkupKVPattern = regexp.MustCompile(`(?is)<(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)\b[^>]*>(.*?)</(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)>`)

func parseMarkupKVObject(text string) map[string]any {
	matches := toolCallMarkupKVPattern.FindAllStringSubmatch(strings.TrimSpace(text), -1)
	if len(matches) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		key := strings.TrimSpace(m[1])
		endKey := strings.TrimSpace(m[3])
		if key == "" {
			continue
		}
		if !strings.EqualFold(key, endKey) {
			continue
		}
		value := parseMarkupValue(m[2])
		if value == nil {
			continue
		}
		appendMarkupValue(out, key, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseMarkupValue(inner string) any {
	if value, ok := extractStandaloneCDATA(inner); ok {
		return value
	}
	value := strings.TrimSpace(extractRawTagValue(inner))
	if value == "" {
		return ""
	}

	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		if parsed := parseStructuredToolCallInput(value); len(parsed) > 0 {
			if len(parsed) == 1 {
				if raw, ok := parsed["_raw"].(string); ok {
					return raw
				}
			}
			return parsed
		}
	}

	var jsonValue any
	if json.Unmarshal([]byte(value), &jsonValue) == nil {
		return jsonValue
	}
	return value
}

func appendMarkupValue(out map[string]any, key string, value any) {
	if existing, ok := out[key]; ok {
		switch current := existing.(type) {
		case []any:
			out[key] = append(current, value)
		default:
			out[key] = []any{current, value}
		}
		return
	}
	out[key] = value
}

// extractRawTagValue treats the inner content of a tag robustly.
// It detects CDATA and strips it, otherwise it unescapes standard HTML entities.
// It avoids over-aggressive tag stripping that might break user content.
func extractRawTagValue(inner string) string {
	trimmed := strings.TrimSpace(inner)
	if trimmed == "" {
		return ""
	}

	// 1. Check for CDATA - if present, it's the ultimate "safe" container.
	if value, ok := extractStandaloneCDATA(trimmed); ok {
		return value // Return raw content between CDATA brackets
	}

	// 2. If no CDATA, we still want to be robust.
	// We unescape standard HTML entities (like &lt; &gt; &amp;)
	// but we DON'T recursively strip tags unless they are actually valid XML tags
	// at the start/end (which should have been handled by the outer matcher anyway).

	// If it contains what looks like a single tag and no other text, it might be nested XML
	// but for KV objects we usually want the value.
	return html.UnescapeString(inner)
}

func extractStandaloneCDATA(inner string) (string, bool) {
	trimmed := strings.TrimSpace(inner)
	if openLen := toolCDATAOpenLenAt(trimmed, 0); openLen > 0 {
		if closeStart := findTrailingToolCDATACloseStart(trimmed); closeStart >= openLen {
			return trimmed[openLen:closeStart], true
		}
		if end := findToolCDATAEnd(trimmed, openLen); end >= 0 {
			return trimmed[openLen:end], true
		}
		return trimmed[openLen:], true
	}
	return "", false
}

func parseJSONLiteralValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}

	switch trimmed[0] {
	case '{', '[', '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
	default:
		return nil, false
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// SanitizeLooseCDATA repairs malformed trailing CDATA openings just enough for
// final parsing and flush-time recovery. Properly closed CDATA blocks are left
// untouched; an unclosed opener is stripped so the remaining text can still be
// parsed as part of the surrounding tool markup.
func SanitizeLooseCDATA(text string) string {
	if text == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(text))
	changed := false
	pos := 0
	for pos < len(text) {
		start := indexToolCDATAOpen(text, pos)
		if start < 0 {
			b.WriteString(text[pos:])
			break
		}
		openLen := toolCDATAOpenLenAt(text, start)
		contentStart := start + openLen
		b.WriteString(text[pos:start])

		if endRel := findToolCDATAEnd(text, contentStart); endRel >= 0 {
			end := endRel + toolCDATACloseLenAt(text, endRel)
			b.WriteString(text[start:end])
			pos = end
			continue
		}

		changed = true
		b.WriteString(text[contentStart:])
		pos = len(text)
	}

	if !changed {
		return text
	}
	return b.String()
}
