package executor

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func isCodexInvalidEncryptedContentError(statusCode int, body []byte) bool {
	if statusCode < 400 || len(body) == 0 {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	upstreamCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	if upstreamCode == "invalid_encrypted_content" || strings.Contains(lower, "invalid_encrypted_content") {
		return true
	}
	if !strings.Contains(lower, "encrypted content") {
		return false
	}
	return strings.Contains(lower, "could not be verified") ||
		strings.Contains(lower, "could not be decrypted") ||
		strings.Contains(lower, "decrypted or parsed")
}

func stripCodexEncryptedReasoningState(payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload, false
	}
	input, ok := root["input"].([]any)
	if !ok || len(input) == 0 {
		return payload, false
	}

	cleaned := make([]any, 0, len(input))
	changed := false
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			cleaned = append(cleaned, rawItem)
			continue
		}

		itemType := strings.ToLower(strings.TrimSpace(compatStringValue(item["type"])))
		if itemType == "compaction" {
			if _, hasEncryptedContent := item["encrypted_content"]; hasEncryptedContent {
				changed = true
				continue
			}
		}
		if deleteEncryptedContentFields(item) {
			changed = true
		}
		cleaned = append(cleaned, item)
	}

	if !changed || len(cleaned) == 0 {
		return payload, false
	}
	root["input"] = cleaned
	out, err := json.Marshal(root)
	if err != nil || !json.Valid(out) {
		return payload, false
	}
	return out, true
}

func deleteEncryptedContentFields(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if _, ok := typed["encrypted_content"]; ok {
			delete(typed, "encrypted_content")
			changed = true
		}
		for _, child := range typed {
			if deleteEncryptedContentFields(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if deleteEncryptedContentFields(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}
