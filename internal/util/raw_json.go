package util

import (
	"encoding/json"
	"strings"
)

// RawJSON converts a raw JSON fragment into json.RawMessage and omits empty input.
func RawJSON(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.RawMessage(raw)
}
