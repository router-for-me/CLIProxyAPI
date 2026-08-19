package toolcall

import (
	"encoding/xml"
	"html"
	"strings"
)

func parseStructuredToolCallInput(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}

	if strings.HasPrefix(trimmed, "<") {
		if parsed, ok := parseXMLFragmentValue(trimmed); ok {
			switch v := parsed.(type) {
			case map[string]any:
				if len(v) > 0 {
					return v
				}
				return map[string]any{}
			case string:
				text := strings.TrimSpace(v)
				if text == "" {
					return map[string]any{}
				}
				if parsedText := parseToolCallInput(text); len(parsedText) > 0 {
					if isOnlyRawValue(parsedText, text) {
						// Plain text content, keep it as raw text.
					} else {
						return parsedText
					}
				}
				return map[string]any{"_raw": v}
			}
		}

		if kv := parseMarkupKVObject(trimmed); len(kv) > 0 {
			return kv
		}
	}

	if kv := parseMarkupKVObject(trimmed); len(kv) > 0 {
		return kv
	}

	if parsed := parseToolCallInput(trimmed); len(parsed) > 0 {
		return parsed
	}

	return map[string]any{"_raw": html.UnescapeString(trimmed)}
}

func parseXMLFragmentValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}

	dec := xml.NewDecoder(strings.NewReader("<root>" + trimmed + "</root>"))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	start, ok := tok.(xml.StartElement)
	if !ok || !strings.EqualFold(start.Name.Local, "root") {
		return nil, false
	}

	value, err := parseXMLNodeValue(dec, start)
	if err != nil {
		return nil, false
	}
	return value, true
}

func parseXMLNodeValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	children := map[string]any{}
	var text strings.Builder
	hasChild := false

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			s := string([]byte(t))
			if hasChild && strings.TrimSpace(s) == "" {
				continue
			}
			text.WriteString(s)
		case xml.StartElement:
			if !hasChild && strings.TrimSpace(text.String()) == "" {
				text.Reset()
			}
			hasChild = true
			child, err := parseXMLNodeValue(dec, t)
			if err != nil {
				return nil, err
			}
			appendXMLChildValue(children, t.Name.Local, child)
		case xml.EndElement:
			if t.Name.Local != start.Name.Local {
				return nil, errXMLMismatch(start.Name.Local, t.Name.Local)
			}
			if len(children) == 0 {
				if parsed, ok := parseJSONLiteralValue(text.String()); ok {
					return parsed, nil
				}
				return text.String(), nil
			}
			if txt := text.String(); strings.TrimSpace(txt) != "" {
				if parsed, ok := parseJSONLiteralValue(txt); ok {
					children["_text"] = parsed
				} else {
					children["_text"] = txt
				}
			}
			if len(children) == 1 {
				if items, ok := children["item"]; ok {
					switch v := items.(type) {
					case []any:
						return v, nil
					default:
						return []any{v}, nil
					}
				}
			}
			return children, nil
		}
	}
}

func appendXMLChildValue(dst map[string]any, key string, value any) {
	if key == "" {
		return
	}
	if existing, ok := dst[key]; ok {
		switch current := existing.(type) {
		case []any:
			dst[key] = append(current, value)
		default:
			dst[key] = []any{current, value}
		}
		return
	}
	dst[key] = value
}

func isOnlyRawValue(m map[string]any, raw string) bool {
	if len(m) != 1 {
		return false
	}
	v, ok := m["_raw"].(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(v) == strings.TrimSpace(raw)
}

type xmlMismatchError struct {
	want string
	got  string
}

func (e xmlMismatchError) Error() string {
	return "mismatched xml end tag: want " + e.want + ", got " + e.got
}

func errXMLMismatch(want, got string) error {
	return xmlMismatchError{want: want, got: got}
}
