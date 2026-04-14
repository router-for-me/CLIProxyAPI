package handlers

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeRequestPayload(handlerType string, rawJSON []byte) []byte {
	if handlerType != "claude" || len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return rawJSON
	}

	return normalizeClaudeToolResultContentArrays(rawJSON)
}

// normalizeClaudeToolResultContentArrays keeps Claude requests compatible with
// downstream translators that only accept string tool_result content.
func normalizeClaudeToolResultContentArrays(rawJSON []byte) []byte {
	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if !messagesResult.IsArray() {
		return rawJSON
	}

	normalized := rawJSON
	changed := false

	messagesResult.ForEach(func(messageKey, messageResult gjson.Result) bool {
		contentResult := messageResult.Get("content")
		if !contentResult.IsArray() {
			return true
		}

		contentResult.ForEach(func(contentKey, blockResult gjson.Result) bool {
			if blockResult.Get("type").String() != "tool_result" {
				return true
			}

			toolContentResult := blockResult.Get("content")
			if !toolContentResult.IsArray() {
				return true
			}

			path := "messages." + messageKey.String() + ".content." + contentKey.String() + ".content"
			updated, err := sjson.SetBytes(normalized, path, flattenClaudeToolResultContent(toolContentResult))
			if err == nil {
				normalized = updated
				changed = true
			}
			return true
		})

		return true
	})

	if !changed {
		return rawJSON
	}
	return normalized
}

func flattenClaudeToolResultContent(contentResult gjson.Result) string {
	segments := make([]string, 0)

	contentResult.ForEach(func(_, itemResult gjson.Result) bool {
		segment := ""
		switch {
		case itemResult.Type == gjson.String:
			segment = itemResult.String()
		case itemResult.IsObject() && itemResult.Get("type").String() == "text" && itemResult.Get("text").Type == gjson.String:
			segment = itemResult.Get("text").String()
		default:
			segment = itemResult.Raw
		}

		if segment != "" {
			segments = append(segments, segment)
		}
		return true
	})

	return strings.Join(segments, "\n\n")
}
