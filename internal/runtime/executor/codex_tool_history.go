package executor

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func repairCodexResponsesToolHistory(payload []byte) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "input").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	input, ok := root["input"].([]any)
	if !ok || len(input) == 0 {
		return payload
	}

	allowOrphanOutputs := strings.TrimSpace(compatStringValue(root["previous_response_id"])) != ""
	callPresent := make(map[string]bool)
	outputPresent := make(map[string]bool)
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			continue
		}
		itemType := strings.TrimSpace(compatStringValue(item["type"]))
		callID := strings.TrimSpace(compatStringValue(item["call_id"]))
		if callID == "" {
			continue
		}
		switch {
		case isCodexResponsesToolCallType(itemType):
			if codexResponsesToolCallHasName(item) {
				callPresent[callID] = true
			}
		case isCodexResponsesToolCallOutputType(itemType):
			outputPresent[callID] = true
		}
	}

	cleaned := make([]any, 0, len(input))
	seenCalls := make(map[string]bool)
	seenOutputs := make(map[string]bool)
	changed := false
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			cleaned = append(cleaned, rawItem)
			continue
		}

		itemType := strings.TrimSpace(compatStringValue(item["type"]))
		switch {
		case itemType == "message":
			cleanedItem, keep, itemChanged := sanitizeCodexResponsesMessageItem(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				changed = true
				continue
			}
			cleaned = append(cleaned, cleanedItem)
		case isCodexResponsesToolCallType(itemType):
			cleanedItem, callID, keep, itemChanged := sanitizeCodexResponsesToolCallItem(item)
			if itemChanged {
				changed = true
			}
			if !keep || seenCalls[callID] || !outputPresent[callID] {
				changed = true
				continue
			}
			seenCalls[callID] = true
			cleaned = append(cleaned, cleanedItem)
		case isCodexResponsesToolCallOutputType(itemType):
			callID := strings.TrimSpace(compatStringValue(item["call_id"]))
			if callID == "" || seenOutputs[callID] || (!allowOrphanOutputs && !callPresent[callID]) {
				changed = true
				continue
			}
			seenOutputs[callID] = true
			cleaned = append(cleaned, item)
		default:
			cleaned = append(cleaned, rawItem)
		}
	}

	if !changed {
		return payload
	}
	root["input"] = cleaned
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func sanitizeCodexResponsesMessageItem(item map[string]any) (map[string]any, bool, bool) {
	content, ok := item["content"]
	if !ok || content == nil {
		return item, false, false
	}

	role := strings.TrimSpace(compatStringValue(item["role"]))
	switch value := content.(type) {
	case string:
		return item, strings.TrimSpace(value) != "", false
	case map[string]any:
		part, keep, _ := sanitizeCodexResponsesContentPart(value, role)
		if !keep {
			return item, false, true
		}
		item["content"] = []any{part}
		return item, true, true
	case []any:
		cleaned := make([]any, 0, len(value))
		changed := false
		for _, rawPart := range value {
			part, keep, partChanged := sanitizeCodexResponsesContentPart(rawPart, role)
			if partChanged {
				changed = true
			}
			if !keep {
				changed = true
				continue
			}
			cleaned = append(cleaned, part)
		}
		if len(cleaned) == 0 {
			return item, false, true
		}
		if changed || len(cleaned) != len(value) {
			item["content"] = cleaned
			changed = true
		}
		return item, true, changed
	default:
		return item, false, true
	}
}

func sanitizeCodexResponsesContentPart(rawPart any, role string) (any, bool, bool) {
	switch part := rawPart.(type) {
	case string:
		text := strings.TrimSpace(part)
		if text == "" {
			return rawPart, false, true
		}
		return map[string]any{
			"type": codexResponsesDefaultTextPartType(role),
			"text": part,
		}, true, true
	case map[string]any:
		partType := strings.TrimSpace(compatStringValue(part["type"]))
		switch partType {
		case "input_text", "output_text":
			return part, strings.TrimSpace(compatStringValue(part["text"])) != "", false
		case "input_image":
			if codexResponsesInputImageHasSource(part) {
				return part, true, false
			}
			return part, false, true
		case "input_file":
			if codexResponsesInputFileHasSource(part) {
				return part, true, false
			}
			return part, false, true
		default:
			text := strings.TrimSpace(compatStringValue(part["text"]))
			if text == "" {
				return part, false, true
			}
			part["type"] = codexResponsesDefaultTextPartType(role)
			return part, true, true
		}
	default:
		return rawPart, false, true
	}
}

func sanitizeCodexResponsesToolCallItem(item map[string]any) (map[string]any, string, bool, bool) {
	callID := strings.TrimSpace(compatStringValue(item["call_id"]))
	if callID == "" {
		return item, "", false, false
	}

	name, okName := normalizeOpenAICompatFunctionName(compatStringValue(item["name"]))
	if !okName {
		return item, callID, false, false
	}

	changed := false
	if compatStringValue(item["name"]) != name {
		item["name"] = name
		changed = true
	}
	if strings.TrimSpace(compatStringValue(item["type"])) == "function_call" {
		if _, okArguments := item["arguments"].(string); !okArguments {
			if item["arguments"] == nil {
				item["arguments"] = ""
			} else if rawArguments, err := json.Marshal(item["arguments"]); err == nil {
				item["arguments"] = string(rawArguments)
			} else {
				item["arguments"] = ""
			}
			changed = true
		}
	}
	return item, callID, true, changed
}

func codexResponsesToolCallHasName(item map[string]any) bool {
	_, ok := normalizeOpenAICompatFunctionName(compatStringValue(item["name"]))
	return ok
}

func codexResponsesDefaultTextPartType(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return "output_text"
	}
	return "input_text"
}

func codexResponsesInputImageHasSource(part map[string]any) bool {
	if strings.TrimSpace(compatStringValue(part["image_url"])) != "" || strings.TrimSpace(compatStringValue(part["file_id"])) != "" {
		return true
	}
	if imageURL, ok := part["image_url"].(map[string]any); ok {
		return strings.TrimSpace(compatStringValue(imageURL["url"])) != ""
	}
	return false
}

func codexResponsesInputFileHasSource(part map[string]any) bool {
	for _, key := range []string{"file_id", "file_data", "file_url"} {
		if strings.TrimSpace(compatStringValue(part[key])) != "" {
			return true
		}
	}
	return false
}

func isCodexResponsesToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func isCodexResponsesToolCallOutputType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}
