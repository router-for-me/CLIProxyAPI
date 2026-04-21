package util

import (
	"encoding/json"
	"strings"
)

// RawJSON returns a trimmed copy that can be embedded as a raw JSON fragment.
func RawJSON(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
