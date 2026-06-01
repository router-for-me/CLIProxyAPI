package helps

import (
	"encoding/json"
	"fmt"
)

// RepairInvalidJSONStringEscapes makes malformed JSON string escapes literal.
// Some compatibility gateways forward user text such as \archive or \v inside
// JSON strings without doubling the backslash. Go's JSON encoder would produce
// valid JSON, but pass-through request paths can preserve the malformed bytes.
func RepairInvalidJSONStringEscapes(body []byte) ([]byte, bool) {
	if len(body) == 0 || json.Valid(body) {
		return body, false
	}

	out := make([]byte, 0, len(body)+16)
	changed := false
	inString := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if !inString {
			out = append(out, ch)
			if ch == '"' {
				inString = true
			}
			continue
		}

		switch ch {
		case '"':
			out = append(out, ch)
			inString = false
		case '\\':
			if i+1 >= len(body) {
				out = append(out, '\\', '\\')
				changed = true
				continue
			}
			next := body[i+1]
			switch next {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				out = append(out, ch, next)
				i++
			case 'u':
				if i+5 < len(body) && isJSONHexQuad(body[i+2:i+6]) {
					out = append(out, body[i:i+6]...)
					i += 5
				} else {
					out = append(out, '\\', '\\')
					changed = true
				}
			default:
				out = append(out, '\\', '\\')
				changed = true
			}
		default:
			if ch < 0x20 {
				out = fmt.Appendf(out, "\\u%04x", ch)
				changed = true
				continue
			}
			out = append(out, ch)
		}
	}

	if !changed || !json.Valid(out) {
		return body, false
	}
	return out, true
}

func isJSONHexQuad(value []byte) bool {
	if len(value) != 4 {
		return false
	}
	for _, ch := range value {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}
