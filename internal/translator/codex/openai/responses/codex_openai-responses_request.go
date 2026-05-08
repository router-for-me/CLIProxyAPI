package responses

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	// DEBUG: 打印原始请求
	log.Debugf("codex responses: raw input JSON (first 2000 chars): %s", func() string {
		s := string(rawJSON)
		if len(s) > 2000 {
			return s[:2000] + "..."
		}
		return s
	}())

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() {
		if v.String() != "priority" {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	// Convert reasoning_content to reasoning type for DeepSeek multi-turn compatibility.
	rawJSON = convertReasoningContentToReasoning(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)

	// DEBUG: 打印翻译后的请求
	log.Debugf("codex responses: translated output JSON (first 2000 chars): %s", func() string {
		s := string(rawJSON)
		if len(s) > 2000 {
			return s[:2000] + "..."
		}
		return s
	}())

	return rawJSON
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	// Directly modify role values for items with "system" role
	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

// convertReasoningContentToReasoning traverses the input array and converts
// reasoning_content in assistant messages to reasoning type with summary.
// DeepSeek requires passing reasoning_content back in multi-turn conversations.
func convertReasoningContentToReasoning(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		role := gjson.GetBytes(result, rolePath).String()

		// Only process assistant messages
		if role != "assistant" {
			continue
		}

		// Check if reasoning_content exists
		rcPath := fmt.Sprintf("input.%d.reasoning_content", i)
		rc := gjson.GetBytes(result, rcPath)
		if !rc.Exists() || rc.Type != gjson.String || rc.String() == "" {
			continue
		}

		// Get content array
		contentPath := fmt.Sprintf("input.%d.content", i)
		contentResult := gjson.GetBytes(result, contentPath)
		if !contentResult.IsArray() {
			// Create content array if it doesn't exist
			result, _ = sjson.SetRawBytes(result, contentPath, []byte(`[]`))
		}

		// Add reasoning item to content
		// Find the last index of content array
		contentArray := gjson.GetBytes(result, contentPath).Array()
		lastIdx := len(contentArray)

		// Create reasoning summary item
		reasoningItemPath := fmt.Sprintf("input.%d.content.%d", i, lastIdx)
		reasoningItem := []byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
		reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.text", rc.String())

		result, _ = sjson.SetRawBytes(result, reasoningItemPath, reasoningItem)

		// Delete the original reasoning_content field
		result, _ = sjson.DeleteBytes(result, rcPath)
	}

	return result
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	return result
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}
