package util

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// FlattenClaudeToolResultContent converts structured Claude tool_result content
// blocks into a plain string while preserving non-text blocks as JSON.
func FlattenClaudeToolResultContent(content gjson.Result) string {
	switch {
	case !content.Exists():
		return ""
	case content.Type == gjson.String:
		return content.String()
	case content.IsObject() && content.Get("type").String() == "text" && content.Get("text").Type == gjson.String:
		return content.Get("text").String()
	case !content.IsArray():
		return content.Raw
	}

	var builder strings.Builder
	content.ForEach(func(_, item gjson.Result) bool {
		var segment string
		switch {
		case item.Type == gjson.String:
			segment = item.String()
		case item.IsObject() && item.Get("type").String() == "text" && item.Get("text").Type == gjson.String:
			segment = item.Get("text").String()
		default:
			segment = compactJSON(item.Raw)
		}

		if segment == "" {
			return true
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(segment)
		return true
	})

	return builder.String()
}

func compactJSON(raw string) string {
	if raw == "" || !gjson.Valid(raw) {
		return raw
	}

	var buffer bytes.Buffer
	if err := json.Compact(&buffer, []byte(raw)); err != nil {
		return raw
	}
	return buffer.String()
}
